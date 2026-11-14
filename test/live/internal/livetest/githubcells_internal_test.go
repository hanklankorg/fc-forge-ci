// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// declaresVersion reports whether a project file of the demo declares
// version, the one a hosted release requests unless told another.
func declaresVersion(t *testing.T, consumer, version string) bool {
	t.Helper()

	declares := false

	err := filepath.WalkDir(consumer, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !slices.Contains([]string{"pom.xml", "package.json", "Cargo.toml", "gradle.properties"}, entry.Name()) {
			return err
		}

		body, err := os.ReadFile(path)
		declares = declares || strings.Contains(string(body), version)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return declares
}

// The list is the demos' fixtures: every event a demo ships a hosted
// workflow for is a cell, every cell's workflow is shipped and its settings
// are ones the trial takes, and a release of a demo that declares the
// version a hosted release requests by default asks for another, or it
// would prove no version bump.
func TestGitHubCellsAreTheDemosHostedFixtures(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)

	demos, err := readMatrixDemos(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, demo := range demos {
		for _, event := range append(slices.Clone(githubEvents), TrialLifecycle) {
			if demo.hosted[event] && !slices.ContainsFunc(GitHubCells, func(cell GitHubCell) bool {
				return cell.Demo == demo.name && cell.Event == event && len(cell.Settings) <= 1
			}) {
				t.Errorf("%s ships a hosted %s and the list has no cell of it", demo.name, event)
			}
		}
	}

	seen := map[string]bool{}

	for _, cell := range GitHubCells {
		name := strings.Join(append([]string{cell.Demo, cell.Event}, cell.Settings...), " ")
		if seen[name] {
			t.Errorf("%s: listed twice", name)
		}

		seen[name] = true

		settings, err := ParseGitHubTrialSettings(cell.Event, cell.Settings)
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}

		consumer := demoConsumerDir(root, cell.Demo)

		workflows := []string{settings.Workflow}
		if cell.Event == TrialLifecycle {
			workflows = []string{trialPullRequestWorkflow, GitHubDevelopmentWorkflow, trialReleaseWorkflow}
		}

		fixtures := filepath.FromSlash(settings.Fixtures)
		if fixtures == "" {
			fixtures = filepath.Dir(filepath.FromSlash(githubDemoWorkflows))
		}

		for _, workflow := range workflows {
			body, err := os.ReadFile(filepath.Join(consumer, fixtures, "workflows", workflow))
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}

			// A run of cells gives a variant none of the run's settings, as
			// the lint image; a variant that took one would run without it.
			if cell.variant() && strings.Contains(string(body), "nanolinter-image") {
				t.Errorf("%s: the variant's %s takes the lint image, which a run of cells does not give it", name, workflow)
			}
		}

		releases := cell.Event == TrialRelease || cell.Event == TrialLifecycle
		if releases && settings.Fixtures == "" && declaresVersion(t, consumer, strings.TrimPrefix(settings.Version, "v")) {
			t.Errorf("%s: requests %s, which the demo declares already; the release would bump nothing", name, settings.Version)
		}
	}
}

// The sets as they were approved: the medium one has each service demo as
// one lifecycle with one visibility, every other demo's pull request and
// release, the variants and the small set; the small one a pull request of
// each ecosystem, one release that bumps and one development build.
func TestGitHubCellSetsAreAsApproved(t *testing.T) {
	t.Parallel()

	in := func(set string) []string {
		var cells []string

		for _, cell := range GitHubCells {
			if cell.InSet(set) {
				cells = append(cells, strings.TrimSpace(cell.Demo+" "+cell.Event+" "+cell.Half))
			}
		}

		return cells
	}

	if small, want := in(SetSmall), []string{
		"hello-maven pull-request", "hello-gradle pull-request", "hello-npm-package pull-request", "hello-cargo-cli pull-request",
		"hello-go-cli pull-request", "hello-meta pull-request", "hello-go-service dev-release", "hello-maven-service release",
	}; !slices.Equal(small, want) {
		t.Errorf("small cells %q, want %q", small, want)
	}

	medium := in(SetMedium)
	for _, want := range []string{
		"hello-maven-service lifecycle private", "hello-cargo-container lifecycle private", "hello-gradle-service lifecycle public",
		"hello-npm-service lifecycle public", "hello-go-service lifecycle public", "hello-go-service dev-release",
	} {
		if !slices.Contains(medium, want) {
			t.Errorf("the medium set has no %s", want)
		}
	}

	services := []string{"hello-maven-service", "hello-cargo-container", "hello-gradle-service", "hello-npm-service", "hello-go-service"}

	for _, cell := range GitHubCells {
		service, alone := slices.Contains(services, cell.Demo), cell.Event != TrialLifecycle && len(cell.Settings) <= 1
		// A service demo's events alone are the full set's, but for the
		// small set's development build and release; every other cell is
		// in the medium set.
		if want := !service || !alone || cell.Set == SetSmall; cell.InSet(SetMedium) != want {
			t.Errorf("%s %s %q: in the medium set %t, want %t", cell.Demo, cell.Event, cell.Settings, !want, want)
		}

		if !slices.Contains([]string{SetSmall, SetMedium, SetFull}, cell.Set) {
			t.Errorf("%s %s names no run set, got %q", cell.Demo, cell.Event, cell.Set)
		}

		if !cell.InSet(SetFull) || (cell.InSet(SetSmall) && !cell.InSet(SetMedium)) {
			t.Errorf("%s %s: outside the full set, or in the small one and not the medium one", cell.Demo, cell.Event)
		}
	}
}

// A run of cells is refused before it starts for what a cell would be
// refused for later: a release with no release credential named, and a
// setting an event does not take. The credential's file is never opened.
func TestGitHubCellRunsRefuseWhatACellWouldFailOn(t *testing.T) {
	t.Parallel()

	unset := func(string) string { return "" }
	named := func(name string) string {
		if name == GitHubReleaseTokenFileEnv {
			return "/a/file/that/does/not/exist"
		}

		return ""
	}

	root := reporoot.Path(t)
	lint := githubTestLintImages()

	if _, err := GitHubCellRuns(root, SetSmall, lint, unset); !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), GitHubReleaseTokenFileEnv) {
		t.Errorf("a set with a release and no credential named gave %v", err)
	}

	runs, err := GitHubCellRuns(root, SetMedium, lint, named)
	if err != nil || !slices.Contains(runs, "private\thello-maven-service\tlifecycle\tversion=0.2.0 nanolinter-image="+githubTestLintImage("java")) ||
		!slices.Contains(runs, "public\thello-npm-package\tpull-request\tfixtures=test/live/github-fork head=fork") {
		t.Errorf("medium runs %q, %v", runs, err)
	}

	// A fork's pull request runs public alone, in the full set too.
	if full, err := GitHubCellRuns(root, "", lint, named); err != nil || len(full) != len(GitHubCells) || !strings.HasPrefix(full[0], "both\t") ||
		!slices.Contains(full, "public\thello-npm-package\tpull-request\tfixtures=test/live/github-fork head=fork") {
		t.Errorf("the full set gave %d runs of %d cells, %v", len(full), len(GitHubCells), err)
	}

	// version= is a release's: a pull request refuses it, offline.
	if _, err := GitHubCellRuns(root, SetSmall, append(lint, "version=0.3.0"), named); !errors.Is(err, errs.ErrValidation) {
		t.Errorf("a setting a pull request does not take gave %v", err)
	}
}

// A cell lints with the run's image of the flavor its demo's pull-request
// fixture pins, and a run is refused before it starts for a flavor it names
// no image for, or a lint image not given per flavor.
func TestGitHubCellRunsLintEachDemoWithItsFlavor(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	release := func(name string) string {
		if name == GitHubReleaseTokenFileEnv {
			return "/a/file"
		}

		return ""
	}

	runs, err := GitHubCellRuns(root, SetSmall, githubTestLintImages(), release)
	if err != nil {
		t.Fatal(err)
	}

	for demo, flavor := range map[string]string{"hello-maven": "java", "hello-gradle": "kotlin", "hello-cargo-cli": "rust", "hello-meta": "core"} {
		if !slices.Contains(runs, "both\t"+demo+"\tpull-request\tnanolinter-image="+githubTestLintImage(flavor)) {
			t.Errorf("%s does not lint with the %s image: %q", demo, flavor, runs)
		}
	}

	for name, extra := range map[string][]string{
		"no kotlin image":   slices.DeleteFunc(githubTestLintImages(), func(word string) bool { return strings.Contains(word, "=kotlin:") }),
		"one image for all": {"nanolinter-image=" + githubTestLintImage("java")},
		"a flavor twice":    append(githubTestLintImages(), "nanolinter-image=java:"+githubTestLintImage("java")),
		"no digest":         {"nanolinter-image=java:ghcr.io/o/nanolinter-java:latest"},
	} {
		if _, err := GitHubCellRuns(root, SetSmall, extra, release); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("%s: gave %v", name, err)
		}
	}
}

// githubTestLintImage is a pullable-shaped lint image of a flavor.
func githubTestLintImage(flavor string) string {
	return "ghcr.io/o/nanolinter-" + flavor + "@sha256:" + strings.Repeat("a", 64)
}

// githubTestLintImages names an image for every flavor the fixtures pin.
func githubTestLintImages() []string {
	flavors := []string{"core", "go", "java", "javascript-typescript", "kotlin", "rust", "swift"}
	words := make([]string, 0, len(flavors))

	for _, flavor := range flavors {
		words = append(words, "nanolinter-image="+flavor+":"+githubTestLintImage(flavor))
	}

	return words
}
