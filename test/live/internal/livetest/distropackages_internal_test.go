// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The Go CLI demo staged with its packages fixtures is the example's
// GoReleaser release in place of its ordinary one: one release workflow,
// which builds with GoReleaser and has the layer sign distro packages, the
// declaration that publishes them, and the configuration and locked tools
// the build needs, each at the root. The plan a trial makes of it renders.
// Without the fixtures the demo is the release it was.
func TestForgejoFixturesStageTheDemosPackagesRelease(t *testing.T) {
	t.Parallel()

	_, demoRoot, source, git := stageTestDemo(t, "hello-go-cli")

	ordinary, err := source.workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	staged, err := source.withFixtures(t.Context(), ForgejoPackagesFixtures)
	if err != nil {
		t.Fatal(err)
	}

	workflows, err := staged.workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(workflows) != len(ordinary) || workflows[trialPullRequestWorkflow] != ordinary[trialPullRequestWorkflow] {
		t.Errorf("the fixtures changed more workflows than the release: %d, was %d", len(workflows), len(ordinary))
	}

	release := workflows[trialReleaseWorkflow]
	if release == ordinary[trialReleaseWorkflow] || !strings.Contains(release, "/build-goreleaser@") || strings.Count(release, "\n      sign-distro-packages: 'true'\n") != 2 {
		t.Errorf("the staged release workflow is not the GoReleaser release that signs packages:\n%s", release)
	}

	if _, err = sampleLayer().PlanConsumer(workflows, releaseFacts()); err != nil {
		t.Errorf("the staged demo does not render: %v", err)
	}

	for file, holds := range map[string]string{
		DefaultArtifactsConfig: "distro-packages:",
		".goreleaser.yml":      "nfpms:",
		".mise.toml":           `"aqua:goreleaser/goreleaser"`,
		"mise.lock":            `[[tools."aqua:goreleaser/goreleaser"]]`,
	} {
		body, showErr := git.run(t.Context(), staged.stage, "show", staged.Commit+":"+file)
		if showErr != nil || !strings.Contains(body, holds) {
			t.Errorf("%s of the staged demo holds no %q (%v)", file, holds, showErr)
		}
	}

	// The host reads the staged consumer where it is written out: the
	// packages its release publishes, and the project it bumps.
	root := t.TempDir()
	if err = staged.export(t.Context(), root); err != nil {
		t.Fatal(err)
	}

	if packages, planErr := ForgejoPlannedDistroPackages(root, trialReleaseWorkflow); planErr != nil || !slices.Equal(packages, []string{"my-cli"}) {
		t.Errorf("the staged demo publishes %v, %v; want my-cli", packages, planErr)
	}

	if bumps, planErr := ForgejoPlannedVersionBumps(root, trialReleaseWorkflow); planErr != nil {
		t.Errorf("the staged demo's version bumps: %v, %v", bumps, planErr)
	}

	if err = git.requireClean(t.Context(), demoRoot, "."); err != nil {
		t.Errorf("writing the staged consumer changed its checkout: %v", err)
	}

	if _, err = source.withFixtures(t.Context(), "test/live/no-such-fixtures"); !errors.Is(err, errForgejoReleaseFixture) {
		t.Errorf("fixtures of no file: %v, want a refusal", err)
	}
}

// What a trial reads of a release's distro packages on the host: the demo's
// ordinary release signs none and publishes none, and its packages release
// signs them and publishes the package its GoReleaser configuration names.
func TestForgejoDistroPackagesOfTheDemo(t *testing.T) {
	t.Parallel()

	demo := demoConsumerDir(reporoot.Path(t), "hello-go-cli")

	for name, test := range map[string]struct {
		root     string
		signs    bool
		packages []string
	}{
		"ordinary release": {demo, false, nil},
		"packages release": {filepath.Join(demo, filepath.FromSlash(ForgejoPackagesFixtures)), true, []string{"my-cli"}},
	} {
		signs, err := ForgejoSignsDistroPackages(test.root, trialReleaseWorkflow)
		if err != nil || signs != test.signs {
			t.Errorf("%s: signs distro packages %t, %v; want %t", name, signs, err, test.signs)
		}

		packages, err := ForgejoPlannedDistroPackages(test.root, trialReleaseWorkflow)
		if err != nil || !slices.Equal(packages, test.packages) {
			t.Errorf("%s: publishes %v, %v; want %v", name, packages, err, test.packages)
		}
	}

	// A registry lists a version as the format spells it; another version
	// that only begins alike is not the release's.
	listed := []string{"0.4.0", "0.4.0-1", "0.4.0-r0", "0.4.00", "0.4.1", "10.4.0"}
	if got := DistroPackageVersions(listed, "0.4.0"); !slices.Equal(got, listed[:3]) {
		t.Errorf("versions of 0.4.0 among %v: %v", listed, got)
	}
}

// A release trial takes the consumer's fixtures once, as a path inside it;
// no other event stages any.
func TestTrialSettingsTakeFixturesForARelease(t *testing.T) {
	t.Parallel()

	settings, err := ParseTrialSettings(TrialRelease, []string{"fixtures=" + ForgejoPackagesFixtures})
	if err != nil || settings.Fixtures != ForgejoPackagesFixtures {
		t.Errorf("fixtures %q, %v", settings.Fixtures, err)
	}

	for name, test := range map[string]struct {
		event string
		words []string
	}{
		"pull request":  {TrialPullRequest, []string{"fixtures=" + ForgejoPackagesFixtures}},
		"twice":         {TrialRelease, []string{"fixtures=one", "fixtures=two"}},
		"absolute":      {TrialRelease, []string{"fixtures=/etc"}},
		"outside":       {TrialRelease, []string{"fixtures=../other"}},
		"without value": {TrialRelease, []string{"fixtures="}},
	} {
		if _, err := ParseTrialSettings(test.event, test.words); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// What a release added to a registry is removed when its test ends, and
// nothing else: the versions it read back, as the registry spells them, and
// not another that only looks like the release's. The removal runs when the
// test's own context is over, so a cleanup that lists or deletes with that
// context removes nothing and fails here. The trial is a parallel subtest, so
// it is over, cleanup and all, before the cleanup of this test judges it.
func TestDistroPackagesOfARunAreRemovedAtCleanup(t *testing.T) {
	t.Parallel()

	before := []string{"0.3.0", "0.4.0-1"}
	listed := []string{"0.3.0", "0.4.0-1", "0.4.0", "0.4.0-r0"}

	created := NewPackageVersions(before, listed)
	if !slices.Equal(created, []string{"0.4.0", "0.4.0-r0"}) {
		t.Fatalf("a release that added 0.4.0 and 0.4.0-r0 created %v", created)
	}

	target := Target{Forge: provider.ForgeForgejo, Host: "packages.invalid", Owner: "fixture"}

	// A registry of one package, which answers no request whose context is
	// over.
	requests := func(ctx context.Context) packageRequest {
		return func(method, endpoint string, into any, _ ...int) error {
			if err := ctx.Err(); err != nil {
				return err
			}

			parsed, err := url.Parse(endpoint)
			if err != nil {
				return err
			}

			if method == http.MethodDelete {
				listed = slices.DeleteFunc(listed, func(version string) bool { return version == path.Base(parsed.Path) })

				return nil
			}

			packages := make([]publishedPackage, 0, len(listed))
			for _, version := range listed {
				packages = append(packages, publishedPackage{Name: "my-cli", Version: version, Type: "debian"})
			}

			body, err := json.Marshal(packages)
			if err != nil {
				return err
			}

			return json.Unmarshal(body, into)
		}
	}

	var over error

	t.Cleanup(func() {
		if over == nil {
			t.Error("the test's context was still open when its cleanup ran, so this proves nothing")

			return
		}

		if !slices.Equal(listed, before) {
			t.Errorf("after the cleanup the registry lists %v, want %v", listed, before)
		}
	})

	t.Run("trial", func(t *testing.T) {
		t.Parallel()

		deletePublishedPackagesAtCleanup(t, target, "fci-repo", "debian", "my-cli", created, requests)
		// Registered after it, so it runs first: the state the removal starts in.
		t.Cleanup(func() { over = t.Context().Err() })
	})
}
