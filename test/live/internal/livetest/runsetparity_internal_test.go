// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// coveragePair is what a run set covers of a demo: one event of it, where a
// lifecycle is its pull request, development build and release, and a
// snapshot is a development build.
type coveragePair struct{ demo, event string }

func (pair coveragePair) String() string { return pair.demo + " " + pair.event }

// coveragePairs are the pairs a case or cell of demo and event covers.
func coveragePairs(demo, event string) []coveragePair {
	switch event {
	case TrialLifecycle:
		return []coveragePair{{demo, TrialPullRequest}, {demo, TrialDevRelease}, {demo, TrialRelease}}
	case TrialSnapshot:
		return []coveragePair{{demo, TrialDevRelease}}
	default:
		return []coveragePair{{demo, event}}
	}
}

// setCoverage is the pairs forge's table or cells cover in set. A release
// option and a fixture or declaration variant are a forge's own axis, and
// cover no pair.
func setCoverage(forge, set string) map[coveragePair]bool {
	covered := map[coveragePair]bool{}

	add := func(demo, event string) {
		for _, pair := range coveragePairs(demo, event) {
			covered[pair] = true
		}
	}

	if forge == matrixGitHub {
		for _, cell := range GitHubCells {
			if cell.InSet(set) && !cell.variant() {
				add(cell.Demo, cell.Event)
			}
		}

		return covered
	}

	for _, c := range PreparedCases(forge) {
		if c.InSet(set) && c.Option == "" && c.Fixtures == "" && c.SnapshotDeclaration == "" {
			add(c.Demo, c.Event)
		}
	}

	return covered
}

// smallCoverage is the small set on every forge: a pull request of each
// ecosystem, one release that bumps the version and builds an image, and
// one development build.
var smallCoverage = []coveragePair{
	{"hello-cargo-cli", TrialPullRequest},
	{"hello-go-cli", TrialPullRequest},
	{"hello-go-service", TrialDevRelease},
	{"hello-gradle", TrialPullRequest},
	{"hello-maven", TrialPullRequest},
	{"hello-maven-service", TrialRelease},
	{"hello-meta", TrialPullRequest},
	{"hello-npm-package", TrialPullRequest},
}

// runSetExceptions are the pairs a set has on some forges and not on the
// others (absent), each with its reason; "missing:" names what a forge would
// need first.
var runSetExceptions = []struct {
	demo, event string
	sets        []string
	absent      []string
	reason      string
}{
	{"hello-ios", TrialPullRequest, []string{SetMedium, SetFull}, []string{forgeForgejo, forgeGitLab},
		"the iOS build needs a macOS runner, which only hosted GitHub has"},
	{"hello-go-service", TrialRelease, []string{SetMedium, SetFull}, []string{forgeGitLab},
		"the demo ships no GitLab release build (.gitlab/build-release.sh)"},
	{"hello-android", TrialPullRequest, []string{SetMedium, SetFull}, []string{forgeForgejo},
		"missing: the demo's Forgejo workflows (.forgejo/workflows) and an Android job image on Forgejo"},
	{"hello-android-library", TrialPullRequest, []string{SetMedium, SetFull}, []string{forgeForgejo},
		"missing: the demo's Forgejo workflows (.forgejo/workflows) and an Android job image on Forgejo"},
	{"hello-android-library", TrialRelease, []string{SetMedium, SetFull}, []string{forgeForgejo, forgeGitLab},
		"missing: a Forgejo release workflow and a GitLab release build (.gitlab/build-release.sh) of the demo"},
	{"hello-base-images", TrialRelease, []string{SetMedium, SetFull}, []string{matrixGitHub},
		"missing: a hosted GitHub release fixture of the demo (test/live/github/workflows)"},
	{"hello-cargo-workspace", TrialDevRelease, []string{SetMedium, SetFull}, []string{matrixGitHub},
		"missing: the demo's hosted development-build fixture (test/live/github/workflows/release-dev.yml)"},
	{"hello-go-cli", TrialDevRelease, []string{SetMedium, SetFull}, []string{matrixGitHub},
		"missing: the demo's hosted development-build fixture (test/live/github/workflows/release-dev.yml)"},
	{"hello-monorepo", TrialDevRelease, []string{SetMedium, SetFull}, []string{matrixGitHub},
		"missing: the demo's hosted development-build fixture (test/live/github/workflows/release-dev.yml)"},
	{"hello-npm-package", TrialDevRelease, []string{SetMedium, SetFull}, []string{matrixGitHub},
		"missing: the demo's hosted development-build fixture (test/live/github/workflows/release-dev.yml)"},
}

// byName orders pairs by demo, then event.
func byName(a, b coveragePair) int { return strings.Compare(a.String(), b.String()) }

// Every run set covers the same pairs on Forgejo, GitLab and hosted GitHub,
// but for the named exceptions, each of which must still hold: its pair in
// the set on the other forges and on none of those it names. The small set
// is smallCoverage on every forge.
func TestRunSetsCoverTheSamePairsOnEveryForge(t *testing.T) {
	t.Parallel()

	forges := []string{forgeForgejo, forgeGitLab, matrixGitHub}

	for _, set := range []string{SetSmall, SetMedium, SetFull} {
		t.Run(set, func(t *testing.T) {
			t.Parallel()

			covered := map[string]map[coveragePair]bool{}
			union := map[coveragePair]bool{}

			for _, forge := range forges {
				covered[forge] = setCoverage(forge, set)
				maps.Copy(union, covered[forge])
			}

			absent := map[coveragePair][]string{}

			for _, exception := range runSetExceptions {
				pair := coveragePair{exception.demo, exception.event}
				if !slices.Contains(exception.sets, set) {
					continue
				}

				absent[pair] = slices.Sorted(slices.Values(exception.absent))

				if !union[pair] {
					t.Errorf("the exception for %s (%s) no longer applies: no forge covers it in this set", pair, exception.reason)
				}
			}

			for _, pair := range slices.SortedFunc(maps.Keys(union), byName) {
				var missing []string

				for _, forge := range forges {
					if !covered[forge][pair] {
						missing = append(missing, forge)
					}
				}

				if want := absent[pair]; !slices.Equal(slices.Sorted(slices.Values(missing)), want) {
					t.Errorf("%s: absent on %q, the exceptions say %q", pair, missing, want)
				}
			}

			if set != SetSmall {
				return
			}

			for _, forge := range forges {
				if got := slices.SortedFunc(maps.Keys(covered[forge]), byName); !slices.Equal(got, smallCoverage) {
					t.Errorf("%s: the small set covers %q, want %q", forge, got, smallCoverage)
				}
			}
		})
	}
}
