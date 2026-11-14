// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The app builds its APKs and AAB, the library its AAR: neither signs, and
// both run their tests.
func TestAndroidDemosBindBothJobsToPreparedEngine(t *testing.T) {
	t.Parallel()

	for demo, variant := range map[string][2]string{
		"hello-android":         {"include-aab", "true"},
		"hello-android-library": {"library", "true"},
	} {
		t.Run(demo, func(t *testing.T) {
			t.Parallel()

			body := reporoot.ReadFile(t, "test/live/fixtures/consumers/"+demo+"/consumer/test/live/github/workflows/pullrequest.yml")
			rendered, _, err := RenderGitHubConsumer("pullrequest.yml", string(body), trialEngine, nil)
			require.NoError(t, err)

			var workflow struct {
				Jobs map[string]struct {
					Uses string
					If   string
					With map[string]string
				}
			}
			require.NoError(t, yaml.Unmarshal([]byte(rendered), &workflow))

			for job, leaf := range map[string]string{"pr-checks": "pullrequest-orchestrator.yml", "build": "build-gradle-android.yml"} {
				call, ok := workflow.Jobs[job]
				require.True(t, ok, job)
				require.Equal(t, trialEngine.Owner+"/"+trialEngine.Repository+"/.github/workflows/"+leaf+"@"+trialEngine.Commit, call.Uses)
				require.Equal(t, trialEngine.Commit, call.With["forge-ci-binary-ref"], "missing binary ref would resolve latest stable instead of the prepared engine")
			}

			build := workflow.Jobs["build"]
			require.Equal(t, "${{ github.event_name == 'workflow_dispatch' }}", build.If)
			require.Equal(t, "ghcr.io/example-org/forge-ci-runtime-android-35@"+trialEngine.Digests["forge-ci-runtime-android-35"], build.With["runtime-image"])
			require.Equal(t, "false", build.With["enable-signing"])
			require.Equal(t, "false", build.With["skip-tests"])
			require.Equal(t, variant[1], build.With[variant[0]])
		})
	}
}

// The library's release as its declaration plans it and the hosted harness
// reads it: the build uploads the AAR under the artifact's name and the
// exported Maven publication beside it, the names the publish stage downloads
// by; the declared GPG method keeps the trial from rekeying the release to
// Cosign; the oracle reads a Maven package back; and the fixture hands the
// orchestrator the Android image of the build and the Java image of the
// publication.
func TestAndroidLibraryDemoReleasePublishesItsMavenPublication(t *testing.T) {
	t.Parallel()

	consumer := "test/live/fixtures/consumers/hello-android-library/consumer/"
	declaration := string(reporoot.ReadFile(t, consumer+".forge-ci/artifacts.yml"))
	fixture := string(reporoot.ReadFile(t, consumer+"test/live/github/workflows/release.yml"))

	cfg, err := parseServiceDeclaration(declaration)
	require.NoError(t, err)

	plan := pipeline.NewConfigPlan(cfg)
	require.Len(t, plan.Artifacts.ForgePackages, 1)

	library := plan.Artifacts.ForgePackages[0]
	require.Equal(t, config.BuildTypeLibrary, library.BuildType)
	require.Equal(t, "hello-android-library", library.BuildArtifactName, "the AAR's workflow artifact")
	require.Equal(t, "hello-android-library-maven-publications", library.PublicationArtifactName)
	require.Equal(t, domainrelease.SignMethodGPG, plan.Sign.Method)
	require.False(t, plan.Sign.RequiresSigstoreEgress)
	require.False(t, ReleaseWritesPublicLog(declaration), "without a declared GPG method the trial signs with its Cosign key instead")

	staged := t.TempDir()
	writeFile(t, filepath.Join(staged, ".forge-ci", "artifacts.yml"), declaration)
	writeFile(t, filepath.Join(staged, ".github", "workflows", "release.yml"), fixture)

	maven, err := githubReleasesMavenPackages(staged, "release.yml")
	require.NoError(t, err)
	require.True(t, maven, "the release oracle must read the Maven package back")

	rendered, _, err := RenderGitHubConsumer("release.yml", fixture, trialEngine, nil)
	require.NoError(t, err)

	var workflow struct {
		Jobs map[string]struct {
			Uses string
			With map[string]string
		}
	}
	require.NoError(t, yaml.Unmarshal([]byte(rendered), &workflow))

	release := workflow.Jobs["release"]
	require.Equal(t, trialEngine.Owner+"/"+trialEngine.Repository+"/.github/workflows/release-orchestrator.yml@"+trialEngine.Commit, release.Uses)

	for input, image := range map[string]string{"runtime-image-android": "forge-ci-runtime-android-35", "runtime-image-java": "forge-ci-runtime-java-25"} {
		require.Equal(t, "ghcr.io/example-org/"+image+"@"+trialEngine.Digests[image], release.With[input])
	}
}
