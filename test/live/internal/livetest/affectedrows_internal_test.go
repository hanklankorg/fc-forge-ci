// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"slices"
	"strings"
	"testing"
)

// affectedCases are the cases of forge's table that the launcher runs for
// the rows AffectedRows gives for a change of paths, by name, and the
// scenarios among the rows. Every row must select a case or name a scenario.
func affectedCases(t *testing.T, forge string, paths ...string) ([]string, []string) {
	t.Helper()

	rows, _, err := AffectedRows(forge, SetFull, paths)
	if err != nil {
		t.Fatal(err)
	}

	var cases, scenarios []string

	for _, row := range rows {
		filters, err := runFilters(row)
		if err != nil {
			t.Fatal(err)
		}

		selects := false

		for _, c := range PreparedCases(forge) {
			name := append([]string{PreparedCaseTest(forge, c.Event)}, strings.Split(c.Name(), "/")...)
			if _, selected := filters[0].row(name, c.Visibilities(SetFull, "")); selected {
				cases, selects = append(cases, c.Name()), true
			}
		}

		if !selects {
			scenarios = append(scenarios, strings.Trim(row, "^$"))
		}
	}

	// The tables' order is not the rows': a lane's cases are not adjacent.
	slices.Sort(cases)

	return cases, scenarios
}

func caseNames(forge string, keep func(PreparedCase) bool) []string {
	var names []string

	for _, c := range PreparedCases(forge) {
		if keep(c) {
			names = append(names, c.Name())
		}
	}

	slices.Sort(names)

	return names
}

// Coverage is never lost to a missing entry: a path the table does not know
// can affect every row of every forge.
func TestAffectedRowsOfAPathWithNoEntryAreEveryRow(t *testing.T) {
	t.Parallel()

	const unknown = "a-directory-nobody-listed/file"

	for _, forge := range []string{forgeForgejo, forgeGitLab} {
		cases, scenarios := affectedCases(t, forge, "docs/live-runs.md", unknown)
		if want := caseNames(forge, func(PreparedCase) bool { return true }); !slices.Equal(cases, want) {
			t.Errorf("%s: cases %q, want every case %q", forge, cases, want)
		}

		for _, lane := range PreparedLanes {
			for _, scenario := range lane.Scenarios {
				if lane.Forge == forge && lane.Name != laneInterruptionManualGitLab && !slices.Contains(scenarios, scenario) {
					t.Errorf("%s: scenario %s is not among %q", forge, scenario, scenarios)
				}
			}
		}
	}

	cells, everything, err := AffectedRows(matrixGitHub, SetFull, []string{unknown})
	if err != nil || !slices.Contains(cells, "hello-go-cli release") || !slices.Contains(cells, "hello-go-service lifecycle") {
		t.Errorf("github cells %q, %v", cells, err)
	}

	if want := []string{unknown + ": no entry of the table"}; !slices.Equal(everything, want) {
		t.Errorf("paths of every row %q, want %q", everything, want)
	}

	// The engine's directory has no entry: every forge's jobs source its
	// library, and download the binary its checksums name.
	for _, engine := range []string{".engine/bin/lib-forge-ci.sh", ".engine/bin/forge-ci-linux-amd64.sha256"} {
		cases, _ := affectedCases(t, forgeForgejo, engine)
		if want := caseNames(forgeForgejo, func(PreparedCase) bool { return true }); !slices.Equal(cases, want) {
			t.Errorf("%s: cases %q, want every case", engine, cases)
		}
	}
}

func TestAffectedRowsAreNarrowedByTheTable(t *testing.T) {
	t.Parallel()

	snapshot := func(c PreparedCase) bool { return slices.Contains(snapshotEvents, c.Event) }
	goCLI := func(c PreparedCase) bool { return c.Demo == "hello-go-cli" }

	for _, test := range []struct {
		name, forge string
		paths       []string
		cases       func(PreparedCase) bool
		scenarios   []string
	}{
		{"pages, records and unit tests", forgeForgejo, []string{"docs/testing.md", "CHANGELOG.md", "internal/app/release/release_test.go", "test/workflowguard/x_test.go"},
			func(PreparedCase) bool { return false }, nil},
		{"another forge's workflow", forgeGitLab, []string{".forgejo/workflows/release-images.yml", ".github/workflows/build-go.yml"},
			func(PreparedCase) bool { return false }, nil},
		{"Forgejo's rerun helper on GitLab", forgeGitLab, []string{"test/live/internal/livetest/forgejorerun.go"},
			func(PreparedCase) bool { return false }, nil},
		{"Forgejo's rerun helper", forgeForgejo, []string{"test/live/internal/livetest/forgejorerun.go"},
			func(PreparedCase) bool { return true },
			[]string{"TestForgejoRelease_InterruptedRecovery", "TestForgejoRelease_RejectsPreseededProvenance", forgejoNativeSummaryTest, forgejoRerunTest}},
		{"the snapshot workflow", forgeForgejo, []string{".forgejo/workflows/snapshot-images.yml"}, snapshot, nil},
		{"the snapshot script GitLab reads too", forgeGitLab, []string{".forgejo/bin/snapshot-images.sh"}, snapshot, nil},
		{"a demo, and the scenarios that run on it", forgeForgejo, []string{demosDir + "/hello-go-cli/consumer/go.mod"}, goCLI,
			[]string{"TestForgejoRelease_InterruptedRecovery", "TestForgejoRelease_RejectsPreseededProvenance", forgejoRerunTest}},
		{"two changes", forgeForgejo, []string{"examples/go-cli/forgejo/release.yml", ".forgejo/bin/snapshot-images.sh"},
			func(c PreparedCase) bool { return goCLI(c) || snapshot(c) },
			[]string{"TestForgejoRelease_InterruptedRecovery", "TestForgejoRelease_RejectsPreseededProvenance", forgejoRerunTest}},
	} {
		cases, scenarios := affectedCases(t, test.forge, test.paths...)
		if want := caseNames(test.forge, test.cases); !slices.Equal(cases, want) {
			t.Errorf("%s: cases %q, want %q", test.name, cases, want)
		}

		if !slices.Equal(scenarios, test.scenarios) {
			t.Errorf("%s: scenarios %q, want %q", test.name, scenarios, test.scenarios)
		}
	}

	// A lane whose every case is affected is one pattern, and a case its own.
	rows, _, err := AffectedRows(forgeForgejo, SetFull, []string{demosDir + "/hello-base-images/consumer/Containerfile"})
	if want := []string{"^TestForgejoLayer_ConsumerTrial$/^base-graph$"}; err != nil || !slices.Equal(rows, want) {
		t.Errorf("rows %q, %v; want %q", rows, err, want)
	}

	cells, _, err := AffectedRows(matrixGitHub, SetFull, []string{demosDir + "/hello-maven/consumer/pom.xml", "templates/check.yml"})
	if want := []string{"hello-maven pull-request", "hello-maven release version=0.2.0"}; err != nil || !slices.Equal(cells, want) {
		t.Errorf("github cells %q, %v; want %q", cells, err, want)
	}

	// A run set has its own rows alone, and the small one no scenario: the
	// demo's snapshot is the medium set's.
	rows, _, err = AffectedRows(forgeForgejo, SetSmall, []string{demosDir + "/hello-go-cli/consumer/go.mod"})
	if want := []string{"^TestForgejoLayer_ConsumerTrial$/^private$/^hello-go-cli$/^pull-request$/^kaniko$"}; err != nil || !slices.Equal(rows, want) {
		t.Errorf("small rows %q, %v; want %q", rows, err, want)
	}

	if _, _, err := AffectedRows("gitea", SetFull, nil); err == nil {
		t.Error("an unknown forge was accepted")
	}
}
