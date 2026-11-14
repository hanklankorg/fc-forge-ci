// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// GitHubCell is one hosted GitHub trial of a shipped demo, as `just
// test-live-consumer github <prepared>/demos/<demo> <event> <setting>...`
// runs it. Hosted GitHub has no prepared lane; this list is its table.
type GitHubCell struct {
	Demo, Event string
	// Settings are the words the cell needs beside the organization and the
	// lint image, which are the run's: a version, a variant's fixture root
	// and workflow, a released image to verify.
	Settings []string
	// Set and Half are a PreparedCase's: the smallest run set the cell is
	// in, SetFull for a cell of the full one alone, and the one visibility
	// it runs with below the full set, empty for both.
	Set, Half string
	// Only is the one visibility the cell can run with, in every set: a
	// fork's pull request runs on a public repository alone.
	Only string
}

// githubBump asks a release for another version than the 0.1.0 the demo
// declares. A hosted release requests 0.1.0 unless told, which bumps nothing
// in such a demo and so proves no version bump.
const githubBump = "version=0.2.0"

// GitHubCells are the hosted GitHub cells, kept by hand: each demo's fixture
// workflows under test/live/github/workflows by event, the lifecycle of each
// demo that ships all three, and the variants, each a fixture root of its
// own. A test holds the list to the fixtures the demos ship.
//
// The small set is one pull request of each ecosystem, one release that
// bumps, the Maven service's, and one development build alone. The medium set has each service
// demo as one lifecycle, which runs its pull request, development build and
// release, with one visibility; every other demo's pull request and
// release; and the variants. Each set covers the demos' events the lab
// forges' sets do (PreparedCase.Set), but for the named exceptions a test
// keeps.
var GitHubCells = []GitHubCell{
	{Demo: "hello-maven-service", Event: TrialLifecycle, Settings: []string{githubBump}, Set: SetMedium, Half: visibilityPrivate},
	{Demo: "hello-cargo-container", Event: TrialLifecycle, Settings: []string{githubBump}, Set: SetMedium, Half: visibilityPrivate},
	{Demo: "hello-gradle-service", Event: TrialLifecycle, Settings: []string{githubBump}, Set: SetMedium, Half: visibilityPublic},
	{Demo: "hello-npm-service", Event: TrialLifecycle, Settings: []string{githubBump}, Set: SetMedium, Half: visibilityPublic},
	// The Go demos and hello-meta declare no version, so any version serves.
	{Demo: "hello-go-service", Event: TrialLifecycle, Set: SetMedium, Half: visibilityPublic},

	{Demo: "hello-maven", Event: TrialPullRequest, Set: SetSmall},
	{Demo: "hello-gradle", Event: TrialPullRequest, Set: SetSmall},
	{Demo: "hello-npm-package", Event: TrialPullRequest, Set: SetSmall},
	{Demo: "hello-cargo-cli", Event: TrialPullRequest, Set: SetSmall},
	{Demo: "hello-go-cli", Event: TrialPullRequest, Set: SetSmall},
	{Demo: "hello-meta", Event: TrialPullRequest, Set: SetSmall},
	{Demo: "hello-maven-reactor", Event: TrialPullRequest, Set: SetMedium},
	{Demo: "hello-cargo-workspace", Event: TrialPullRequest, Set: SetMedium},
	{Demo: "hello-monorepo", Event: TrialPullRequest, Set: SetMedium},
	{Demo: "hello-android", Event: TrialPullRequest, Set: SetMedium},
	{Demo: "hello-android-library", Event: TrialPullRequest, Set: SetMedium},
	{Demo: "hello-ios", Event: TrialPullRequest, Set: SetMedium},

	{Demo: "hello-maven", Event: TrialRelease, Settings: []string{githubBump}, Set: SetMedium},
	{Demo: "hello-gradle", Event: TrialRelease, Settings: []string{githubBump}, Set: SetMedium},
	{Demo: "hello-npm-package", Event: TrialRelease, Settings: []string{githubBump}, Set: SetMedium},
	{Demo: "hello-cargo-cli", Event: TrialRelease, Settings: []string{githubBump}, Set: SetMedium},
	{Demo: "hello-go-cli", Event: TrialRelease, Set: SetMedium},
	{Demo: "hello-meta", Event: TrialRelease, Set: SetMedium},
	{Demo: "hello-maven-reactor", Event: TrialRelease, Settings: []string{githubBump}, Set: SetMedium},
	{Demo: "hello-cargo-workspace", Event: TrialRelease, Settings: []string{githubBump}, Set: SetMedium},
	{Demo: "hello-monorepo", Event: TrialRelease, Settings: []string{githubBump}, Set: SetMedium},
	{Demo: "hello-android-library", Event: TrialRelease, Settings: []string{githubBump}, Set: SetMedium},

	// The variants: a release with no SBOM bundle, one that prepares no
	// commit, one signed with GPG, one whose files GPG signs and whose image
	// Cosign does, and a pull request from a fork.
	{Demo: "hello-go-cli", Event: TrialRelease, Settings: []string{"fixtures=test/live/github-no-sboms", "workflow=release-no-sboms.yml"}, Set: SetMedium},
	{Demo: "hello-go-cli", Event: TrialRelease, Settings: []string{"fixtures=test/live/github-no-changelog", "workflow=release-no-changelog.yml"}, Set: SetMedium},
	{Demo: "hello-go-cli", Event: TrialRelease, Settings: []string{"fixtures=test/live/github-gpg", "workflow=release-gpg.yml"}, Set: SetMedium},
	{Demo: "hello-go-service", Event: TrialRelease, Settings: []string{"fixtures=test/live/github-gpg-files", "workflow=release-gpg-files.yml", "image=my-service"}, Set: SetMedium},
	{Demo: "hello-npm-package", Event: TrialPullRequest, Settings: []string{"fixtures=test/live/github-fork", "head=fork"}, Set: SetMedium, Only: visibilityPublic},

	// The small set's development build, the one below the full set.
	{Demo: "hello-go-service", Event: TrialDevRelease, Set: SetSmall},

	// The service demos' events alone: their lifecycles run each in the
	// medium set. The Maven service's release is the small set's release.
	{Demo: "hello-maven-service", Event: TrialPullRequest, Set: SetFull},
	{Demo: "hello-maven-service", Event: TrialDevRelease, Set: SetFull},
	{Demo: "hello-maven-service", Event: TrialRelease, Settings: []string{githubBump}, Set: SetSmall},
	{Demo: "hello-cargo-container", Event: TrialPullRequest, Set: SetFull},
	{Demo: "hello-cargo-container", Event: TrialDevRelease, Set: SetFull},
	{Demo: "hello-cargo-container", Event: TrialRelease, Settings: []string{githubBump}, Set: SetFull},
	{Demo: "hello-gradle-service", Event: TrialPullRequest, Set: SetFull},
	{Demo: "hello-gradle-service", Event: TrialDevRelease, Set: SetFull},
	{Demo: "hello-gradle-service", Event: TrialRelease, Settings: []string{githubBump}, Set: SetFull},
	{Demo: "hello-npm-service", Event: TrialPullRequest, Set: SetFull},
	{Demo: "hello-npm-service", Event: TrialDevRelease, Set: SetFull},
	{Demo: "hello-npm-service", Event: TrialRelease, Settings: []string{githubBump}, Set: SetFull},
	{Demo: "hello-go-service", Event: TrialPullRequest, Set: SetFull},
	{Demo: "hello-go-service", Event: TrialRelease, Set: SetFull},
}

// InSet reports whether the cell runs in set.
func (cell GitHubCell) InSet(set string) bool {
	return PreparedCase{Set: cell.Set}.InSet(set)
}

// Visibilities are the visibilities the cell runs with in set, less those it
// ran with in the run set after, as PreparedCase.Visibilities gives them,
// and of those the one it can run with when it has one (Only).
func (cell GitHubCell) Visibilities(set, after string) []string {
	return setDelta(func(set string) []string {
		return slices.DeleteFunc(visibilitiesIn(PreparedCase{Set: cell.Set, Half: cell.Half}, set), func(visibility string) bool {
			return cell.Only != "" && visibility != cell.Only
		})
	}, set, after)
}

// variant reports whether the cell runs a fixture root of its own.
func (cell GitHubCell) variant() bool {
	return slices.ContainsFunc(cell.Settings, func(setting string) bool { return strings.HasPrefix(setting, "fixtures=") })
}

// GitHubCellRuns are the cells of a run set as `just test-live-github-cells`
// runs them, one a line: the visibility ("both", the cell's one in a set
// below the full one, or the only one it can run with), the demo, the event and the cell's settings with the
// run's own (extra), tab-separated, the settings space-separated. The run
// names its lint images per flavor, nanolinter-image=<flavor>:<reference> as
// the prepare's job-image= does, and a cell gets the image of the flavor its
// demo's pull-request fixture pins (githubFixtureLintFlavor). A variant takes
// its own settings alone. A delta run
// (FORGE_CI_LIVE_AFTER) has what set runs and its set after did not
// (GitHubCell.Visibilities): the cells outside the set after, and of a cell
// the set after ran with one visibility, the other one.
//
// Everything a cell would be refused for later is refused here, before
// anything runs: a setting its event does not take, a lint flavor the run
// names no image for, and a release or a lifecycle with no release
// credential named. The credential's file is the
// trial's to read; only its variable is looked at.
func GitHubCellRuns(root, set string, extra []string, getenv func(string) string) ([]string, error) {
	set, err := ParseLiveSet(set)
	if err != nil {
		return nil, err
	}

	lintImages, extra, err := githubCellLintImages(extra)
	if err != nil {
		return nil, err
	}

	after, err := ParseLiveAfter(set, getenv(liveAfterEnv))
	if err != nil {
		return nil, err
	}

	var runs []string

	for _, cell := range GitHubCells {
		visibilities := cell.Visibilities(set, after)
		if len(visibilities) == 0 {
			continue
		}

		// A variant's fixture root has workflows of its own, and none takes
		// the run's settings: the trial refuses a setting no fixture passes.
		words := slices.Clone(cell.Settings)
		if !cell.variant() {
			lint, err := githubCellLintImage(root, cell.Demo, lintImages)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", cell.Demo, cell.Event, err)
			}

			words = append(append(words, extra...), lint...)
		}

		if _, err := ParseGitHubTrialSettings(cell.Event, words); err != nil {
			return nil, fmt.Errorf("%s %s: %w", cell.Demo, cell.Event, err)
		}

		if (cell.Event == TrialRelease || cell.Event == TrialLifecycle) && getenv(GitHubReleaseTokenFileEnv) == "" {
			return nil, fmt.Errorf("%s %s releases: set %s to the release credential's file, as a release of `just test-live-consumer github` needs: %w",
				cell.Demo, cell.Event, GitHubReleaseTokenFileEnv, errs.ErrUsage)
		}

		half := "both"
		if len(visibilities) == 1 {
			half = visibilities[0]
		}

		runs = append(runs, strings.Join([]string{half, cell.Demo, cell.Event, strings.Join(words, " ")}, "\t"))
	}

	return runs, nil
}

// githubFixtureLintFlavor is the Nanolinter flavor a demo's pull-request
// fixture pins its placeholder lint image of, nanolinter:<flavor>@sha256:0...,
// until a run names a real image. It reads the pin githubPlaceholderLintImage
// refuses in a trial; keep the two in step.
var githubFixtureLintFlavor = regexp.MustCompile(`(?m)^[ \t]*nanolinter-image:[ \t]*\S+/nanolinter:([a-z][a-z0-9-]*)@sha256:0{64}\b`)

// githubCellLintImages takes the run's nanolinter-image=<flavor>:<reference>
// words out of extra, one image a flavor, and returns them by flavor with the
// other words.
func githubCellLintImages(extra []string) (map[string]string, []string, error) {
	images := map[string]string{}

	var rest []string

	for _, word := range extra {
		value, lint := strings.CutPrefix(word, "nanolinter-image=")
		if !lint {
			rest = append(rest, word)

			continue
		}

		flavor, image, found := strings.Cut(value, ":")
		if !found || !jobImageFlavor.MatchString(flavor) || !pinnedImage.MatchString(image) {
			return nil, nil, fmt.Errorf("setting %q: want nanolinter-image=<flavor>:<repository>@sha256:<digest>, one a flavor: %w", word, errs.ErrUsage)
		}

		if _, twice := images[flavor]; twice {
			return nil, nil, fmt.Errorf("setting %q: the %s flavor is named twice: %w", word, flavor, errs.ErrUsage)
		}

		images[flavor] = image
	}

	return images, rest, nil
}

// githubCellLintImage is the nanolinter-image= setting of a demo's cell: the
// run's image of the flavor its pull-request fixture pins, or none when the
// fixture lints with no Nanolinter image. Every demo with a cell ships that
// fixture, so a missing one is an error.
func githubCellLintImage(root, demo string, images map[string]string) ([]string, error) {
	fixture := filepath.Join(demoConsumerDir(root, demo), "test", "live", "github", "workflows", "pullrequest.yml")

	body, err := os.ReadFile(fixture) //nolint:gosec // G304: a shipped demo's fixture, under the repository root.
	if err != nil {
		return nil, err
	}

	pinned := githubFixtureLintFlavor.FindSubmatch(body)
	if pinned == nil {
		return nil, nil
	}

	flavor := string(pinned[1])

	image, named := images[flavor]
	if !named {
		return nil, fmt.Errorf("its fixture lints with the %s flavor: pass nanolinter-image=%s:<repository>@sha256:<digest> of an image GitHub's runners can pull: %w",
			flavor, flavor, errs.ErrUsage)
	}

	return []string{"nanolinter-image=" + image}, nil
}
