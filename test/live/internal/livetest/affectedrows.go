// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// liveEffect is one line of liveEffects: changed paths of the repository and
// the live rows a change of one of them can affect.
type liveEffect struct {
	// Prefix and Suffix name the paths: every path that has both. A
	// directory's prefix ends in a slash.
	Prefix, Suffix string
	// Nothing: the change can affect no row.
	Nothing bool
	// Forges, Demos and Events narrow the rows to those of one of each;
	// empty, every forge, demo or event. An entry that sets none of the four
	// says the change can affect every row.
	Forges, Demos, Events []string
	// DemoDir: the path's element after Prefix names the one demo.
	DemoDir bool
}

// unitTestSuffix ends the name of a Go test file.
const unitTestSuffix = "_test.go"

// snapshotEvents are the events that build a snapshot: the snapshot itself,
// the lifecycle, whose development phase it is, and hosted GitHub's name for
// it.
var snapshotEvents = []string{TrialSnapshot, TrialLifecycle, TrialDevRelease}

// liveEffects say which live rows a changed path can affect. The first entry
// that has the path decides, so a narrower entry stands above a wider one. A
// path no entry has can affect every row: a missing entry costs a longer
// run, never a row.
//
// The table is kept by hand from what reads what, and is narrow only where
// that is plain from the tree: a forge's own workflows, a demo's own files, a
// package one forge's commands alone import. It does not follow Go imports;
// the engine's code is one entry.
var liveEffects = []liveEffect{
	// Pages and records no job and no scenario reads. The engine embeds the
	// pages, which changes its bytes and nothing a row does.
	{Prefix: "docs/", Suffix: ".md", Nothing: true},
	{Prefix: "CHANGELOG.md", Nothing: true},
	{Prefix: "README.md", Nothing: true},
	{Prefix: "test/live/evidence.txt", Nothing: true},
	// Unit tests: no build of the engine or of the harness's rows has them.
	{Prefix: "internal/", Suffix: unitTestSuffix, Nothing: true},
	{Prefix: "cmd/", Suffix: unitTestSuffix, Nothing: true},
	{Prefix: "scripts/", Suffix: unitTestSuffix, Nothing: true},
	{Prefix: "test/live/internal/livetest/", Suffix: unitTestSuffix, Nothing: true},
	// The generator of docs/testing-matrices.md and of the evidence draft.
	{Prefix: "test/live/internal/livetest/testing", Nothing: true},

	// Forgejo's layer. GitLab's jobs read three of its files: the snapshot
	// script (templates/snapshot-image-jobs.yml), and the changelog versions
	// and the signer check (.gitlab/bin/release.sh).
	{Prefix: ".forgejo/workflows/snapshot-images.yml", Forges: []string{forgeForgejo}, Events: snapshotEvents},
	{Prefix: ".forgejo/bin/snapshot-images.sh", Forges: []string{forgeForgejo, forgeGitLab}, Events: snapshotEvents},
	{Prefix: ".forgejo/actions/render-changelog/", Forges: []string{forgeForgejo, forgeGitLab}},
	{Prefix: ".forgejo/scripts/", Forges: []string{forgeForgejo, forgeGitLab}},
	{Prefix: ".forgejo/", Forges: []string{forgeForgejo}},

	// GitLab's layer. Every forge's runtime image carries the changelog
	// templates.
	{Prefix: "templates/gitcliff/"},
	{Prefix: "templates/snapshot-image", Forges: []string{forgeGitLab}, Events: snapshotEvents},
	{Prefix: ".gitlab/bin/snapshot-images.sh", Forges: []string{forgeGitLab}, Events: snapshotEvents},
	{Prefix: "templates/", Forges: []string{forgeGitLab}},
	{Prefix: ".gitlab/", Forges: []string{forgeGitLab}},
	{Prefix: "internal/app/gitlabpipeline/", Forges: []string{forgeGitLab}},

	// GitHub's layer.
	{Prefix: ".github/", Forges: []string{matrixGitHub}},

	// The engine, and what its jobs install and run in.
	{Prefix: "internal/"},
	{Prefix: "cmd/"},
	{Prefix: "go.mod"},
	{Prefix: "go.sum"},
	{Prefix: "containers/"},
	{Prefix: "scripts/bootstrap/"},

	// The demos: a demo's own files, and the example whose CI files a demo
	// shows (exampledemos_internal_test.go).
	{Prefix: demosDir + "/", DemoDir: true},
	{Prefix: "examples/base-images/", Demos: []string{"hello-base-images"}},
	{Prefix: "examples/go-cli/", Demos: []string{"hello-go-cli"}},
	{Prefix: "examples/go-service/", Demos: []string{"hello-go-service"}},
	{Prefix: "examples/gradle-app/", Demos: []string{"hello-gradle"}},
	{Prefix: "examples/no-build/", Demos: []string{"hello-meta"}},
	{Prefix: "examples/monorepo/", Demos: []string{"hello-monorepo"}},

	// The harness, by forge where the file's name says it; the rest of it,
	// the launcher included, is every row's.
	{Prefix: "test/live/conformance/forgejo", Forges: []string{forgeForgejo}},
	{Prefix: "test/live/conformance/gitlab", Forges: []string{forgeGitLab}},
	{Prefix: "test/live/internal/livetest/gitlab", Forges: []string{forgeGitLab}},
	{Prefix: "test/live/internal/livetest/forgejorerun.go", Forges: []string{forgeForgejo}},
	{Prefix: "test/live/internal/livetest/github", Forges: []string{matrixGitHub}},
	{Prefix: "test/live/githubconsumer/", Forges: []string{matrixGitHub}},
	{Prefix: "test/live/"},
	// The other tiers' tests, which no live row runs.
	{Prefix: "test/", Nothing: true},
}

// effectOf is the effect of a change of path: the first entry of liveEffects
// that has it, and whether one has. No entry is every row.
func effectOf(path string) (liveEffect, bool) {
	for _, effect := range liveEffects {
		if !strings.HasPrefix(path, effect.Prefix) || !strings.HasSuffix(path, effect.Suffix) {
			continue
		}

		if effect.DemoDir {
			demo, _, _ := strings.Cut(strings.TrimPrefix(path, effect.Prefix), "/")
			effect.Demos = []string{demo}
		}

		return effect, true
	}

	return liveEffect{}, false
}

// everything reports whether the effect is every row.
func (effect liveEffect) everything() bool {
	return !effect.Nothing && len(effect.Forges)+len(effect.Demos)+len(effect.Events) == 0
}

// affects reports whether the effect has the rows of the demo's event on
// forge. A scenario is asked for with no event, and with no demo unless it
// runs on one: an effect of some events or demos alone has no such scenario.
func (effect liveEffect) affects(forge, demo, event string) bool {
	has := func(values []string, value string) bool { return len(values) == 0 || slices.Contains(values, value) }

	return !effect.Nothing && has(effect.Forges, forge) && has(effect.Demos, demo) && has(effect.Events, event)
}

// AffectedRows are the live rows of forge that a change of paths can affect,
// for `just test-live-affected`, and the paths whose effect is every row,
// each with the reason. On forgejo and gitlab a row is a pattern that `just
// test-live-prepared` takes: a lane of a table's test when the change can
// affect each of its cases, else a case, and a lane's scenario by its name.
// The scenario that takes the operator's hand is left to its own recipe. On
// github a row is `<demo> <event>`, a cell `just test-live-consumer github`
// runs, of the list GitHubCells, with the cell's settings.
//
// Only the rows of the run set are given, and no scenario in the small one:
// the scenarios are the medium set's.
func AffectedRows(forge, set string, paths []string) ([]string, []string, error) {
	set, err := ParseLiveSet(set)
	if err != nil {
		return nil, nil, err
	}

	effects := make([]liveEffect, 0, len(paths))

	var everything []string

	for _, path := range paths {
		effect, listed := effectOf(path)
		effects = append(effects, effect)

		switch {
		case !listed:
			everything = append(everything, path+": no entry of the table")
		case effect.everything():
			everything = append(everything, path+": the entry "+effect.Prefix)
		}
	}

	affected := func(demo, event string) bool {
		return slices.ContainsFunc(effects, func(effect liveEffect) bool { return effect.affects(forge, demo, event) })
	}

	switch {
	case isLabForge(forge):
		return affectedPreparedRows(forge, set, affected), everything, nil
	case forge == matrixGitHub:
		return affectedHostedCells(set, affected), everything, nil
	default:
		return nil, nil, fmt.Errorf("the forge is forgejo, gitlab or github, got %q: %w", forge, errs.ErrUsage)
	}
}

// affectedPreparedRows are the patterns of the affected cases of forge's
// table that are in set, in the table's order, then of the affected
// scenarios of its lanes.
func affectedPreparedRows(forge, set string, affected func(demo, event string) bool) []string {
	lane := func(c PreparedCase) string {
		return "^" + PreparedCaseTest(forge, c.Event) + "$/^" + regexp.QuoteMeta(c.Lane) + "$"
	}

	cases, selected := map[string]int{}, map[string]int{}

	inSet := slices.DeleteFunc(slices.Clone(PreparedCases(forge)), func(c PreparedCase) bool { return !c.InSet(set) })

	for _, c := range inSet {
		cases[lane(c)]++

		if affected(c.Demo, c.Event) {
			selected[lane(c)]++
		}
	}

	var rows []string

	for _, c := range inSet {
		if !affected(c.Demo, c.Event) {
			continue
		}

		// A case's pattern ends at its builder, so it runs the case's
		// release options too: a change is never narrowed to one of them.
		row := lane(c)
		if selected[row] != cases[row] {
			row += "/^" + regexp.QuoteMeta(c.Demo) + "$/^" + c.Event + "$/^" + c.Builder + "$"
		}

		if !slices.Contains(rows, row) {
			rows = append(rows, row)
		}
	}

	for _, prepared := range PreparedLanes {
		if prepared.Forge != forge || prepared.Name == laneInterruptionManualGitLab || set == SetSmall {
			continue
		}

		for _, scenario := range prepared.Scenarios {
			if affected(preparedScenarioDemos[scenario], "") {
				rows = append(rows, "^"+scenario+"$")
			}
		}
	}

	return rows
}

// affectedHostedCells are the affected cells of hosted GitHub in set.
func affectedHostedCells(set string, affected func(demo, event string) bool) []string {
	var cells []string

	for _, cell := range GitHubCells {
		if cell.InSet(set) && affected(cell.Demo, cell.Event) {
			cells = append(cells, strings.Join(append([]string{cell.Demo, cell.Event}, cell.Settings...), " "))
		}
	}

	return cells
}
