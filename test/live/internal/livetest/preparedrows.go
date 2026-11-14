// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// FocusedRowRuns are the `go test -run` patterns of the rows a focused run's
// pattern selects in the tables of the run's provider scope, one a row, in
// the tables' order. The launcher runs the test binary once for each, so a
// row prints its verdict when it ends and the binary's time limit is a
// row's, not a lane's. A pattern that selects no row has none, and is
// refused when it names a table's test (noRowOfATable).
//
// The run set (FORGE_CI_LIVE_SET) narrows the rows to its cases, each with
// the visibility it runs with in the set, and a delta run's set after
// (FORGE_CI_LIVE_AFTER) takes out what that set ran. A pattern whose rows
// are all outside the set, or all ran in the set after, is refused: the
// launcher would otherwise run their tests whole.
func FocusedRowRuns(run string) ([]string, error) {
	scope, err := liveProviderScope()
	if err != nil {
		return nil, err
	}

	set, after, err := liveSetAndAfter()
	if err != nil {
		return nil, err
	}

	var rows []string

	selects := false

	for _, forge := range []string{forgeForgejo, forgeGitLab} {
		if scope != "" && string(scope) != forge {
			continue
		}

		selected, err := preparedRowRuns(forge, run, set, after)
		if err != nil {
			return nil, err
		}

		rows = append(rows, selected...)

		if all, _ := preparedRowRuns(forge, run, SetFull, ""); len(all) != 0 {
			selects = true
		}

		if err := noRowOfATable(forge, run); err != nil {
			return nil, err
		}
	}

	if selects && len(rows) == 0 {
		return nil, fmt.Errorf("the run pattern %q selects no row of the %s: %w", run, setName(set, after), errs.ErrUsage)
	}

	return rows, nil
}

// liveSetAndAfter are the run set of a prepared run and the set its delta
// comes after, none for a run of the whole set.
func liveSetAndAfter() (string, string, error) {
	set, err := ParseLiveSet(os.Getenv(liveSetEnv))
	if err != nil {
		return "", "", err
	}

	after, err := ParseLiveAfter(set, os.Getenv(liveAfterEnv))

	return set, after, err
}

// setName names a run's set as a refusal does: "medium set", or "medium set
// after small" for a delta run.
func setName(set, after string) string {
	if after == "" {
		return set + " set"
	}

	return set + " set after " + after
}

// FocusedScenarioRun is the `go test -run` pattern of what a focused run's
// pattern selects beside the tables' rows. The full set runs everything the
// pattern matches. A set below it keeps the scenarios an alternative of the
// pattern names exactly, as ^<test>$ or ^(<test>|<test>)$, and the medium
// set also the lanes' scenarios (PreparedLanes) of the provider scope the
// pattern matches, so a broad pattern runs no scenario that waits for an
// operator's inputs and skips. A delta run leaves out what its set after
// kept: the full set's as FocusedScenarioSkip says, a smaller set's here.
// Empty means nothing beside the rows, and a run where the pattern selects
// no row and no scenario of the delta either is refused.
func FocusedScenarioRun(run string) (string, error) {
	scope, err := liveProviderScope()
	if err != nil {
		return "", err
	}

	set, after, err := liveSetAndAfter()
	if err != nil || (set == SetFull && after == "") {
		return run, err
	}

	filters, err := runFilters(run)
	if err != nil {
		return "", err
	}

	forges, rows := scopeRows(scope, run, set, after)

	if set == SetFull {
		// What the full set runs beside the rows is what the pattern
		// matches, and only a scenario it names exactly is known to have
		// run in the set after.
		broad := slices.ContainsFunc(filters, func(filter runFilter) bool {
			_, exact := exactTests(filter[0])

			return !exact
		})
		if rows == 0 && !broad {
			return "", fmt.Errorf("the run pattern %q selects no row or scenario of the %s: %w", run, setName(set, after), errs.ErrUsage)
		}

		return run, nil
	}

	ran := scenarioAlternatives(filters, after, forges)
	alternatives := slices.DeleteFunc(scenarioAlternatives(filters, set, forges), func(alternative string) bool { return slices.Contains(ran, alternative) })

	if rows == 0 && len(alternatives) == 0 {
		return "", fmt.Errorf("the run pattern %q selects no row or scenario of the %s; name a scenario as ^<test>$: %w", run, setName(set, after), errs.ErrUsage)
	}

	return strings.Join(alternatives, "|"), nil
}

// FocusedScenarioSkip is the `go test -skip` pattern of the scenarios a
// delta run of the full set leaves out beside the rows, those its set after
// kept (FocusedScenarioRun); empty for any other run. The alternatives of
// fewer levels come first: testing takes the first alternative that
// matches a name, even where it matches only the start of a deeper one, and
// such a match skips nothing.
func FocusedScenarioSkip(run string) (string, error) {
	scope, err := liveProviderScope()
	if err != nil {
		return "", err
	}

	set, after, err := liveSetAndAfter()
	if err != nil || set != SetFull || after == "" {
		return "", err
	}

	filters, err := runFilters(run)
	if err != nil {
		return "", err
	}

	forges, _ := scopeRows(scope, run, set, after)
	alternatives := scenarioAlternatives(filters, after, forges)

	slices.SortStableFunc(alternatives, func(one, other string) int {
		return strings.Count(one, "/") - strings.Count(other, "/")
	})

	return strings.Join(alternatives, "|"), nil
}

// scenarioAlternatives are the patterns of the scenarios the alternatives
// of a pattern keep in a set below the full one (filterScenarios), each
// once; none for no set.
func scenarioAlternatives(filters []runFilter, set string, forges []string) []string {
	if set == "" {
		return nil
	}

	var alternatives []string

	for _, filter := range filters {
		for _, alternative := range filterScenarios(filter, set, forges) {
			if !slices.Contains(alternatives, alternative) {
				alternatives = append(alternatives, alternative)
			}
		}
	}

	return alternatives
}

// scopeRows are the forges of the provider scope, both when it is empty,
// and the number of rows of their tables that run selects in set after the
// set after.
func scopeRows(scope provider.ForgeAPI, run, set, after string) ([]string, int) {
	var (
		forges []string
		rows   int
	)

	for _, forge := range []string{forgeForgejo, forgeGitLab} {
		if scope == "" || string(scope) == forge {
			forges = append(forges, forge)
			selected, _ := preparedRowRuns(forge, run, set, after)
			rows += len(selected)
		}
	}

	return forges, rows
}

// filterScenarios are the patterns of the scenarios one alternative of a
// focused run's pattern keeps in a set below the full one, each anchored
// and followed by what the alternative says of deeper levels: the tests
// outside the tables it names exactly, or else, in the medium set, the
// lanes' scenarios of forges that its first level matches.
func filterScenarios(filter runFilter, set string, forges []string) []string {
	levels := make([]string, 0, len(filter))
	for _, level := range filter[1:] {
		levels = append(levels, level.String())
	}

	deeper := strings.Join(append([]string{""}, levels...), "/")

	var scenarios []string

	if tests, exact := exactTests(filter[0]); exact {
		for _, test := range tests {
			if !isTableTest(test) {
				scenarios = append(scenarios, "^"+test+"$"+deeper)
			}
		}

		return scenarios
	}

	if set != SetMedium {
		return nil
	}

	for _, lane := range PreparedLanes {
		if !slices.Contains(forges, lane.Forge) || lane.Name == laneInterruptionManualGitLab {
			continue
		}

		for _, scenario := range lane.Scenarios {
			if filter[0].MatchString(scenario) {
				scenarios = append(scenarios, "^"+scenario+"$"+deeper)
			}
		}
	}

	return scenarios
}

// exactTests are the tests a level names exactly, as ^<test>$ or
// ^(<test>|<test>...)$, each a plain name.
func exactTests(level *regexp.Regexp) ([]string, bool) {
	inner, anchored := strings.CutPrefix(level.String(), "^")
	inner, ended := strings.CutSuffix(inner, "$")

	closed := true
	if grouped, open := strings.CutPrefix(inner, "("); open {
		inner, closed = strings.CutSuffix(grouped, ")")
	}

	// A level holds no | outside parentheses (runFilters), so one here is
	// the group's.
	tests := strings.Split(inner, "|")
	plain := !slices.ContainsFunc(tests, func(test string) bool { return test == "" || regexp.QuoteMeta(test) != test })

	return tests, anchored && ended && closed && plain
}

// isTableTest reports whether test runs the cases of a forge's table, whose
// rows FocusedRowRuns gives.
func isTableTest(test string) bool {
	for _, tests := range preparedCaseTests {
		for _, table := range tests {
			if table == test {
				return true
			}
		}
	}

	return false
}

// noRowOfATable refuses an alternative of run that names a test of forge's
// table exactly, as ^<test>$, goes on below it and selects no row. Go would
// run the test with no case in it and pass it, so a mistyped lane, demo or
// visibility would end as a run that passed.
func noRowOfATable(forge, run string) error {
	filters, err := runFilters(run)
	if err != nil {
		return err
	}

	for _, filter := range filters {
		selects, table := len(filter) < 2, false

		for _, c := range PreparedCases(forge) {
			test := PreparedCaseTest(forge, c.Event)
			table = table || filter[0].String() == "^"+test+"$"

			if _, selected := filter.row(append([]string{test}, strings.Split(c.Name(), "/")...), c.Visibilities(SetFull, "")); selected {
				selects = true
			}
		}

		if table && !selects {
			return fmt.Errorf("the run pattern %q selects no row of the %s table: %w", run, forge, errs.ErrUsage)
		}
	}

	return nil
}

// preparedRowRuns are the patterns of the cases of forge's table
// (PreparedCases) that run selects, each under the test that runs it
// (PreparedCaseTest). A case that several alternatives of run select has
// one pattern, their alternation, so it runs once with what each selects.
// Only the cases of set are selected, each with its visibilities in the set
// less those it ran with in the set after (PreparedCase.Visibilities).
func preparedRowRuns(forge, run, set, after string) ([]string, error) {
	filters, err := runFilters(run)
	if err != nil {
		return nil, err
	}

	var rows []string

	for _, c := range PreparedCases(forge) {
		visibilities := c.Visibilities(set, after)
		if len(visibilities) == 0 {
			continue
		}

		name := append([]string{PreparedCaseTest(forge, c.Event)}, strings.Split(c.Name(), "/")...)

		var alternatives []string

		for _, filter := range filters {
			if row, selected := filter.row(name, visibilities); selected && !slices.Contains(alternatives, row) {
				alternatives = append(alternatives, row)
			}
		}

		if len(alternatives) != 0 {
			rows = append(rows, strings.Join(alternatives, "|"))
		}
	}

	return rows, nil
}

// runFilter is one alternative of a `go test -run` pattern: a regular
// expression for each level of a test's name, matched unanchored as testing
// matches it.
type runFilter []*regexp.Regexp

// runFilters splits a `go test -run` pattern as testing does: at each `|`
// outside brackets and parentheses into alternatives, and each alternative
// at each such `/` into its levels.
func runFilters(run string) ([]runFilter, error) {
	filters := []runFilter{nil}
	brackets, parentheses, start := 0, 0, 0

	level := func(end int) error {
		expression, err := regexp.Compile(run[start:end])
		if err != nil {
			return fmt.Errorf("the run pattern %q: %w: %w", run, err, errs.ErrValidation)
		}

		filters[len(filters)-1] = append(filters[len(filters)-1], expression)
		start = end + 1

		return nil
	}

	for at := 0; at < len(run); at++ {
		switch run[at] {
		case '[':
			brackets++
		case ']':
			// An unmatched ] is legal.
			brackets = max(brackets-1, 0)
		case '(':
			if brackets == 0 {
				parentheses++
			}
		case ')':
			if brackets == 0 {
				parentheses--
			}
		case '\\':
			at++
		case '/', '|':
			if brackets != 0 || parentheses != 0 {
				continue
			}

			if err := level(at); err != nil {
				return nil, err
			}

			if run[at] == '|' {
				filters = append(filters, nil)
			}
		}
	}

	return filters, level(len(run))
}

// row is the pattern that runs the case named name, <test>/<lane>/<demo>/
// <event>/<builder> and its option if it has one, and nothing else: each
// element anchored, then the visibilities the filter selects, then what the
// filter says of deeper levels. The level below a case is its visibilities
// (ConsumerVisibilities); naming them keeps out the option cases of the same
// demo and builder, whose names go on where this one ends. A case the filter
// leaves no visibility of is not selected. runs are the visibilities the
// case runs with (PreparedCase.Visibilities).
func (filter runFilter) row(name, runs []string) (string, bool) {
	for i, element := range name {
		if i < len(filter) && !filter[i].MatchString(element) {
			return "", false
		}
	}

	visibilities := slices.DeleteFunc(slices.Clone(runs), func(visibility string) bool {
		return len(filter) > len(name) && !filter[len(name)].MatchString(visibility)
	})

	if len(visibilities) == 0 {
		return "", false
	}

	elements := make([]string, 0, max(len(name)+1, len(filter)))
	for _, element := range name {
		elements = append(elements, "^"+regexp.QuoteMeta(element)+"$")
	}

	elements = append(elements, "^("+strings.Join(visibilities, "|")+")$")

	for _, deeper := range filter[min(len(filter), len(name)+1):] {
		elements = append(elements, deeper.String())
	}

	return strings.Join(elements, "/"), true
}
