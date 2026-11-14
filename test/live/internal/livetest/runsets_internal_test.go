// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// checkRunSets holds what each run set of forge's table must keep. The full
// set is the table, and every case names its set. The medium one has every
// lane, and every demo's event with each release option, where a demo's
// lifecycle stands for its pull request and its plain release. What the
// small set covers is the same on every forge
// (TestRunSetsCoverTheSamePairsOnEveryForge).
func checkRunSets(t *testing.T, forge string) {
	t.Helper()

	table := PreparedCases(forge)
	in := func(set string, keep func(PreparedCase) bool) bool {
		return slices.ContainsFunc(table, func(c PreparedCase) bool { return c.InSet(set) && keep(c) })
	}

	for _, c := range table {
		if !slices.Contains([]string{SetSmall, SetMedium, SetFull}, c.Set) {
			t.Errorf("%s: %s names no run set, got %q", forge, c.Name(), c.Set)
		}

		if !in(SetMedium, func(other PreparedCase) bool { return other.Lane == c.Lane }) {
			t.Errorf("%s: the medium set has no case of the lane %s", forge, c.Lane)
		}

		if !in(SetMedium, func(other PreparedCase) bool {
			return other.Demo == c.Demo && other.Option == c.Option &&
				(other.Event == c.Event || (other.Event == TrialLifecycle && c.Event != TrialSnapshot))
		}) {
			t.Errorf("%s: the medium set has no %s of %s with the release option %q", forge, c.Event, c.Demo, c.Option)
		}
	}
}

func TestForgejoRunSetsKeepWhatEachMustCover(t *testing.T) {
	t.Parallel()
	checkRunSets(t, forgeForgejo)
}

func TestGitLabRunSetsKeepWhatEachMustCover(t *testing.T) {
	t.Parallel()
	checkRunSets(t, forgeGitLab)
}

// A run set narrows the rows a pattern selects to its cases, each with the
// visibility it has in the set, and a pattern wholly outside it is refused.
func TestRunSetNarrowsTheRowsOfAPattern(t *testing.T) {
	const lane = "^TestForgejoLayer_ConsumerLifecycle$/^lifecycle$"

	rows, err := preparedRowRuns(forgeForgejo, lane, SetMedium, "")
	if want := []string{
		lane + "/^hello-maven-service$/^lifecycle$/^kaniko$/^(private)$",
		lane + "/^hello-cargo-container$/^lifecycle$/^kaniko$/^(public|private)$",
		lane + "/^hello-npm-service$/^lifecycle$/^buildah$/^(private)$",
		lane + "/^hello-go-service$/^lifecycle$/^buildah$/^(private)$",
		lane + "/^hello-gradle-service$/^lifecycle$/^buildah$/^(public)$",
	}; err != nil || !slices.Equal(rows, want) {
		t.Errorf("medium rows %q, %v; want %q", rows, err, want)
	}

	if full, err := preparedRowRuns(forgeForgejo, lane, SetFull, ""); err != nil || len(full) != 10 || !strings.HasSuffix(full[1], "/^kaniko$/^(public|private)$") {
		t.Errorf("full rows %q, %v; want the lane's ten, each with both visibilities", full, err)
	}

	t.Setenv(liveProviderEnv, forgeForgejo)

	for set, refused := range map[string]bool{SetSmall: true, SetMedium: false, "": false, "tiny": true} {
		t.Setenv(liveSetEnv, set)

		if _, err := FocusedRowRuns(lane); errors.Is(err, errs.ErrUsage) != refused {
			t.Errorf("set %q: the lifecycle lane gave %v, refused %t wanted", set, err, refused)
		}
	}

	if _, err := ParseLiveSet("tiny"); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("an unknown set gave %v", err)
	}
}

// Beside the rows, a set below the full one runs the scenarios the pattern
// names exactly, and the medium set also the lanes' scenarios of the
// provider scope the pattern matches: a broad pattern runs no scenario that
// waits for an operator's inputs. The full set runs what the pattern matches.
func TestRunSetSelectsScenariosBesideTheRows(t *testing.T) {
	const (
		rust     = "TestGitLabLayer_RustDevelopment"
		exact    = "^" + rust + "$"
		capable  = "TestGitLabRelease_BuildImageCapability"
		recovery = "^TestForgejoRelease_InterruptedRecovery$"
	)

	for _, test := range []struct {
		forge, set, run string
		// rows: the pattern selects rows. contains: want is a part of what
		// runs beside them, not all of it.
		rows, contains bool
		want           string
	}{
		{forge: forgeGitLab, set: SetSmall, run: "^TestGitLab", rows: true, want: ""},
		{forge: forgeGitLab, set: SetSmall, run: "^TestGitLab|" + exact, rows: true, want: exact},
		{forge: forgeGitLab, set: SetMedium, run: exact, want: exact},
		{forge: forgeGitLab, set: SetSmall, run: "^(" + rust + "|" + capable + ")$", want: exact + "|^" + capable + "$"},
		{forge: forgeGitLab, set: SetSmall, run: "^(" + rust + "|TestGitLab.*)$", rows: true, want: ""},
		{forge: forgeGitLab, set: SetFull, run: "^TestGitLab|" + exact, rows: true, want: "^TestGitLab|" + exact},
		{forge: forgeGitLab, set: SetMedium, run: "^TestGitLab", rows: true, contains: true, want: "^TestGitLabRelease_MinimalSignedArtifacts$"},
		// The interruption takes the operator's hand and never runs here.
		{forge: forgeGitLab, set: SetMedium, run: "Interrupted", want: "^TestGitLabRelease_ConsumerInterruptedRecovery$"},
		{forge: forgeForgejo, set: SetMedium, run: "Interrupted", want: recovery},
		{forge: forgeForgejo, set: SetSmall, run: "^TestForgejoLayer_", rows: true, want: ""},
		{forge: forgeForgejo, set: SetMedium, run: "^TestForgejoLayer_", rows: true, want: ""},
		{forge: forgeForgejo, set: SetSmall, run: "^TestForgejoLayer_|^TestForgejoLayer_RustDevelopment$", rows: true, want: "^TestForgejoLayer_RustDevelopment$"},
		{forge: forgeForgejo, set: SetMedium, run: "^TestForgejo", rows: true, want: recovery + "|^TestForgejoRelease_RejectsPreseededProvenance$|^" + forgejoRerunTest + "$"},
	} {
		t.Setenv(liveProviderEnv, test.forge)
		t.Setenv(liveSetEnv, test.set)

		if rows, err := FocusedRowRuns(test.run); err != nil || (len(rows) != 0) != test.rows {
			t.Errorf("%s %s: %q selects rows %q, %v", test.forge, test.set, test.run, rows, err)
		}

		others, err := FocusedScenarioRun(test.run)
		if err != nil || (test.contains && !strings.Contains(others, test.want)) || (!test.contains && others != test.want) {
			t.Errorf("%s %s: %q runs %q beside its rows, %v; want %q", test.forge, test.set, test.run, others, err, test.want)
		}
	}

	t.Setenv(liveProviderEnv, forgeGitLab)
	t.Setenv(liveSetEnv, SetSmall)

	if others, err := FocusedScenarioRun("^TestGitLabCheck_"); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("a pattern of neither a row nor a named scenario ran %q, %v", others, err)
	}
}

// deltaRuns are the runs on one commit that together are one run of the
// full set: the small set, the medium set after it and the full set after
// that.
var deltaRuns = [][2]string{{SetSmall, ""}, {SetMedium, SetSmall}, {SetFull, SetMedium}}

// checkDeltaRuns holds that the delta runs, as runs gives each, run every
// case and visibility of the full run exactly once.
func checkDeltaRuns(t *testing.T, name string, runs func(set, after string) []string) {
	t.Helper()

	full := runs(SetFull, "")
	ran := map[string]int{}

	for _, run := range deltaRuns {
		for _, row := range runs(run[0], run[1]) {
			ran[row]++
		}
	}

	for _, row := range full {
		if ran[row] != 1 {
			t.Errorf("%s: %s ran %d times in the delta runs, want once", name, row, ran[row])
		}

		delete(ran, row)
	}

	for row := range ran {
		t.Errorf("%s: the delta runs ran %s, which the full run does not", name, row)
	}
}

// Small, then medium after small, then full after medium are the full run:
// each case of the tables with each of its visibilities once, the other
// half of a case a smaller set ran with one, and so are the hosted cells.
func TestDeltaRunsAreTheFullRun(t *testing.T) {
	t.Parallel()

	for _, forge := range []string{forgeForgejo, forgeGitLab} {
		checkDeltaRuns(t, forge, func(set, after string) []string {
			rows, err := preparedRowRuns(forge, "^Test", set, after)
			if err != nil {
				t.Fatal(err)
			}

			var runs []string

			for _, row := range rows {
				// A row ends in the group of its visibilities, ^(public|private)$.
				at := strings.LastIndex(row, "/")
				for visibility := range strings.SplitSeq(strings.Trim(row[at+1:], "^()$"), "|") {
					runs = append(runs, row[:at]+"/"+visibility)
				}
			}

			return runs
		})
	}

	release := func(name string) string {
		if name == GitHubReleaseTokenFileEnv {
			return "/a/file"
		}

		return ""
	}

	checkDeltaRuns(t, matrixGitHub, func(set, after string) []string {
		cells, err := GitHubCellRuns(reporoot.Path(t), set, githubTestLintImages(), func(name string) string {
			if name == liveAfterEnv {
				return after
			}

			return release(name)
		})
		if err != nil {
			t.Fatal(err)
		}

		var runs []string

		for _, cell := range cells {
			visibility, rest, _ := strings.Cut(cell, "\t")
			if visibility == "both" {
				runs = append(runs, rest+"\t"+visibilityPublic, rest+"\t"+visibilityPrivate)
			} else {
				runs = append(runs, rest+"\t"+visibility)
			}
		}

		return runs
	})
}

// The scenarios of a delta run are those its set runs and its set after
// did not; of the full set, what the pattern matches with those of the set
// after skipped. A pattern that selects nothing of the delta is refused.
func TestDeltaRunSelectsTheScenariosItsSetAfterDidNot(t *testing.T) {
	const (
		rust    = "^TestForgejoLayer_RustDevelopment$"
		lanes   = "^TestForgejoRelease_InterruptedRecovery$|^TestForgejoRelease_RejectsPreseededProvenance$|^" + forgejoRerunTest + "$"
		pattern = "^TestForgejo|" + rust
	)

	t.Setenv(liveProviderEnv, forgeForgejo)

	for _, test := range []struct {
		set, after, run, skip string
	}{
		{set: SetMedium, after: SetSmall, run: lanes},
		{set: SetFull, after: SetSmall, run: pattern, skip: rust},
		{set: SetFull, after: SetMedium, run: pattern, skip: lanes + "|" + rust},
	} {
		t.Setenv(liveSetEnv, test.set)
		t.Setenv(liveAfterEnv, test.after)

		run, err := FocusedScenarioRun(pattern)
		skip, skipErr := FocusedScenarioSkip(pattern)

		if err != nil || skipErr != nil || run != test.run || skip != test.skip {
			t.Errorf("%s after %s: runs %q, %v, skips %q, %v; want %q, skipping %q", test.set, test.after, run, err, skip, skipErr, test.run, test.skip)
		}
	}

	// A deeper alternative comes after the shallower ones, which testing
	// would otherwise not reach for a name the deeper one starts to match.
	t.Setenv(liveSetEnv, SetFull)
	t.Setenv(liveAfterEnv, SetMedium)

	if skip, err := FocusedScenarioSkip(rust + "/^x$|^TestForgejo"); err != nil || skip != lanes+"|"+rust+"/^x$" {
		t.Errorf("full after medium skips %q, %v; want the deeper alternative last", skip, err)
	}

	for _, set := range []string{SetMedium, SetFull} {
		t.Setenv(liveSetEnv, set)
		t.Setenv(liveAfterEnv, SetSmall)

		if run, err := FocusedScenarioRun(rust); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("%s after small: a scenario the small set ran gave %q, %v", set, run, err)
		}
	}
}

// after names a run set below the run's own, or none; anything else is a
// usage error, of the rows and of the hosted cells alike.
func TestDeltaRunRefusesASetAfterNotBelowItsSet(t *testing.T) {
	for _, test := range []struct {
		set, after string
		refused    bool
	}{
		{set: SetMedium, after: SetSmall},
		{set: SetFull, after: SetSmall},
		{set: SetFull, after: SetMedium},
		{set: "", after: SetMedium},
		{set: SetSmall},
		{set: SetSmall, after: SetSmall, refused: true},
		{set: SetMedium, after: SetMedium, refused: true},
		{set: SetFull, after: SetFull, refused: true},
		{set: SetSmall, after: SetMedium, refused: true},
		{set: SetMedium, after: SetFull, refused: true},
		{set: SetFull, after: "tiny", refused: true},
	} {
		t.Setenv(liveProviderEnv, forgeForgejo)
		t.Setenv(liveSetEnv, test.set)
		t.Setenv(liveAfterEnv, test.after)

		_, err := FocusedRowRuns("^TestForgejoLayer_")
		_, cellsErr := GitHubCellRuns(reporoot.Path(t), test.set, githubTestLintImages(), func(name string) string {
			if name == liveAfterEnv {
				return test.after
			}

			return "/a/file"
		})

		if errors.Is(err, errs.ErrUsage) != test.refused || errors.Is(cellsErr, errs.ErrUsage) != test.refused {
			t.Errorf("%q after %q: rows %v, cells %v; refused %t wanted", test.set, test.after, err, cellsErr, test.refused)
		}
	}
}

// A pattern whose every row ran in the set after is refused: the medium
// set after small has nothing of a small case with both visibilities.
func TestDeltaRunRefusesRowsItsSetAfterRan(t *testing.T) {
	index := slices.IndexFunc(ForgejoPreparedCases, func(c PreparedCase) bool { return c.Set == SetSmall && c.Half == "" })
	c := ForgejoPreparedCases[index]

	elements := []string{"^" + PreparedCaseTest(forgeForgejo, c.Event) + "$"}
	for element := range strings.SplitSeq(c.Name(), "/") {
		elements = append(elements, "^"+regexp.QuoteMeta(element)+"$")
	}

	pattern := strings.Join(elements, "/")

	t.Setenv(liveProviderEnv, forgeForgejo)
	t.Setenv(liveSetEnv, SetMedium)

	if rows, err := FocusedRowRuns(pattern); err != nil || len(rows) != 1 {
		t.Fatalf("the medium set: %q selects %q, %v; want its one row", pattern, rows, err)
	}

	t.Setenv(liveAfterEnv, SetSmall)

	if rows, err := FocusedRowRuns(pattern); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("medium after small: %q selects %q, %v; want it refused", pattern, rows, err)
	}
}
