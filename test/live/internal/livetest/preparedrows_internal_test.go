// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const preparedRowsFixtureEnv = "FORGE_CI_TEST_PREPARED_ROWS_FIXTURE"

// The patterns a focused run is split into, by what its pattern selects.
var preparedRowRunCases = []struct {
	name, forge, run string
	want             []string
}{
	{"a lane", forgeGitLab, "^TestGitLabRelease_ConsumerTrial$/^release-variants$", []string{
		"^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-go-cli$/^release$/^buildah$/^prerelease$/^(public|private)$",
		"^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-go-cli$/^release$/^buildah$/^retention$/^(public|private)$",
		"^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-npm-service$/^release$/^buildah$/^(public|private)$",
	}},
	{"a lane of many rows", forgeForgejo, "^TestForgejoLayer_ConsumerTrial$/^release-variants$", nil},
	{"one row", forgeGitLab, "TestGitLabLayer_Snapshot/snapshot/hello-monorepo/snapshot/buildah", []string{
		"^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^(public|private)$",
	}},
	{"one visibility of a row", forgeGitLab, "^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^public$", []string{
		"^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^(public)$",
	}},
	{"one visibility of an option row", forgeGitLab, "^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-go-cli$/^release$/^buildah$/^retention$/^private$", []string{
		"^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-go-cli$/^release$/^buildah$/^retention$/^(private)$",
	}},
	{"below a visibility", forgeGitLab, "^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^private$/^gate$", []string{
		"^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^(private)$/^gate$",
	}},
	{"tests in one group", forgeGitLab, "^(TestGitLabLayer_Snapshot|TestGitLabRelease_ConsumerTrial)$/^release-variants$/hello-go-cli/release/buildah/^prerelease$", []string{
		"^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-go-cli$/^release$/^buildah$/^prerelease$/^(public|private)$",
	}},
	{"two visibilities of a row, each an alternative", forgeGitLab, "^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^public$|^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^private$", []string{
		"^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^(public)$|^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^(private)$",
	}},
	{"alternatives that select one row alike", forgeGitLab, "^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$|TestGitLabLayer_Snapshot/snapshot/hello-monorepo/snapshot/buildah", []string{
		"^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^(public|private)$",
	}},
	{"a row and a lane, each an alternative", forgeGitLab, "^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^public$|^TestGitLabRelease_ConsumerTrial$/^release-variants$", []string{
		"^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^(public)$",
		"^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-go-cli$/^release$/^buildah$/^prerelease$/^(public|private)$",
		"^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-go-cli$/^release$/^buildah$/^retention$/^(public|private)$",
		"^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-npm-service$/^release$/^buildah$/^(public|private)$",
	}},
	{"a scenario", forgeGitLab, "^TestGitLabRelease_MinimalSignedArtifacts$", nil},
	{"no lane of the table", forgeGitLab, "^TestGitLabLayer_Snapshot$/^lifecycle$", nil},
	{"another forge's test", forgeForgejo, "^TestGitLabLayer_Snapshot$/^snapshot$", nil},
}

func TestPreparedRowRunsSelectTheRowsOfTheTables(t *testing.T) {
	t.Parallel()

	for _, tc := range preparedRowRunCases {
		rows, err := preparedRowRuns(tc.forge, tc.run, SetFull, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if tc.name == "a lane of many rows" {
			lane := slices.DeleteFunc(slices.Clone(PreparedCases(tc.forge)), func(c PreparedCase) bool { return c.Lane != LaneReleaseVariants })
			if len(rows) != len(lane) || len(rows) < 3 {
				t.Errorf("%s: %d rows of %d cases", tc.name, len(rows), len(lane))
			}

			continue
		}

		if !slices.Equal(rows, tc.want) {
			t.Errorf("%s: rows\n%s\nwant\n%s", tc.name, strings.Join(rows, "\n"), strings.Join(tc.want, "\n"))
		}
	}

	if _, err := preparedRowRuns(forgeGitLab, "^Test(GitLab$", SetFull, ""); !errors.Is(err, errs.ErrValidation) {
		t.Errorf("a pattern that is no regular expression: %v", err)
	}
}

// A pattern that names a table's test and goes on to select no row of it is
// a usage error that names the pattern: Go would pass the test with no case
// in it. A test of no table, a table's test alone and a test of another
// forge are left to the run, as is every pattern that selects a row.
func TestNoRowOfATableRefusesAPatternThatSelectsNone(t *testing.T) {
	t.Parallel()

	for _, run := range []string{
		"^TestGitLabLayer_Snapshot$/^lifecycle$",
		"^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^internal$",
		"^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^public$|^TestGitLabLayer_Snapshot$/^snapshot$/^no-such-demo$",
	} {
		if err := noRowOfATable(forgeGitLab, run); !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), fmt.Sprintf("%q", run)) {
			t.Errorf("%s: %v, want a usage error naming the pattern", run, err)
		}
	}

	for _, run := range []string{"^TestGitLabRelease_MinimalSignedArtifacts$/^private$", "^TestGitLabLayer_Snapshot$", "TestGitLabLayer_Snap/^lifecycle$"} {
		if err := noRowOfATable(forgeGitLab, run); err != nil {
			t.Errorf("%s: %v", run, err)
		}
	}

	for _, tc := range preparedRowRunCases {
		if err := noRowOfATable(tc.forge, tc.run); (err == nil) != (tc.name != "no lane of the table") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// Go's own -run decides what a pattern runs. The rows of a pattern, each run
// as a process, run the leaves the pattern runs in one process: every one of
// them once, and each process the leaves of one case.
func TestPreparedRowRunsRunWhatThePatternRuns(t *testing.T) {
	t.Parallel()

	for _, tc := range preparedRowRunCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rows, err := preparedRowRuns(tc.forge, tc.run, SetFull, "")
			if err != nil {
				t.Fatal(err)
			}

			var ran []string

			for _, row := range rows {
				leaves := preparedRowsFixtureLeaves(t, tc.forge, row)
				if len(leaves) == 0 {
					t.Errorf("%s runs nothing", row)
				}

				for _, leaf := range leaves {
					if leaf[:strings.LastIndex(leaf, "/")] != leaves[0][:strings.LastIndex(leaves[0], "/")] {
						t.Errorf("%s runs %s beside %s", row, leaf, leaves[0])
					}
				}

				ran = append(ran, leaves...)
			}

			slices.Sort(ran)

			if want := preparedRowsFixtureLeaves(t, tc.forge, tc.run); !slices.Equal(ran, want) {
				t.Errorf("the rows run\n%s\nthe pattern runs\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// preparedRowsFixtureLeaves are the leaves of forge's table that this test
// binary runs for a -run pattern, sorted. The fixture's own name is one more
// level, at the head of each alternative of the pattern.
func preparedRowsFixtureLeaves(t *testing.T, forge, run string) []string {
	t.Helper()

	filters, err := runFilters(run)
	if err != nil {
		t.Fatal(err)
	}

	alternatives := make([]string, 0, len(filters))

	for _, filter := range filters {
		levels := []string{"^TestPreparedRowsFixture$"}
		for _, level := range filter {
			levels = append(levels, level.String())
		}

		alternatives = append(alternatives, strings.Join(levels, "/"))
	}

	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run", strings.Join(alternatives, "|"))

	command.Env = append(os.Environ(), preparedRowsFixtureEnv+"="+forge)

	output, err := command.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", run, err, output)
	}

	var leaves []string

	for line := range strings.SplitSeq(string(output), "\n") {
		if leaf, isLeaf := strings.CutPrefix(line, "leaf TestPreparedRowsFixture/"); isLeaf {
			leaves = append(leaves, leaf)
		}
	}

	slices.Sort(leaves)

	return leaves
}

// TestPreparedRowsFixture has the subtests the live tests of a forge's table
// have, <test>/<case name>/<visibility>, and nothing in them: each leaf
// prints its name.
func TestPreparedRowsFixture(t *testing.T) {
	forge := os.Getenv(preparedRowsFixtureEnv)

	var tests []string

	for _, c := range PreparedCases(forge) {
		if test := PreparedCaseTest(forge, c.Event); !slices.Contains(tests, test) {
			tests = append(tests, test)
		}
	}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			for _, c := range PreparedCases(forge) {
				if PreparedCaseTest(forge, c.Event) != test {
					continue
				}

				t.Run(c.Name(), func(t *testing.T) {
					for _, visibility := range []string{visibilityPublic, visibilityPrivate} {
						t.Run(visibility, func(t *testing.T) { fmt.Println("leaf " + t.Name()) })
					}
				})
			}
		})
	}
}
