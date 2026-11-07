// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/kaniko"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

const releaseJobsArtifacts = `containers:
  - name: app
    container-file: Containerfile
    context: .
    platforms: linux/amd64,linux/arm64
`

func releaseJobsPlanArgs(extra ...string) []string {
	return append([]string{
		"plan", "--file", ".forge-ci/artifacts.yml", "--image-repository", "registry.example/group/app",
		"--repository", "group/app", "--registry-username", "gitlab-ci-token", "--release-tag", "v1.2.3",
	}, extra...)
}

func writeReleaseJobsArtifacts(t *testing.T) {
	t.Helper()

	env := testenv.New(t)
	t.Chdir(env.Temp)
	require.NoError(t, os.MkdirAll(".forge-ci", 0o700))
	require.NoError(t, os.WriteFile(".forge-ci/artifacts.yml", []byte(releaseJobsArtifacts), 0o600))
	require.NoError(t, os.WriteFile("Containerfile", []byte("FROM scratch\n"), 0o600))
	t.Setenv("RELEASE_PLATFORMS", "")
	t.Setenv("IMAGE_PLAN_JSON", "")
}

func TestReleaseImagesPublishDerivesSourceProviderSBOMPrefixBeforeAdmission(t *testing.T) {
	writeReleaseJobsArtifacts(t)
	t.Setenv("FORGE_CI_PROVIDER", "gitlab")
	require.NoError(t, releaseImagesPlanCmd().Run(t.Context(), releaseJobsPlanArgs("--output", "plan.json")))
	for _, server := range []string{"https://gitlab.example", "http://gitlab.example"} {
		err := releaseImagesPublishCmd().Run(t.Context(), []string{
			"publish", "--plan", "plan.json", "--image-repository", "registry.example/group/app",
			"--release-tag", "v1.2.3", "--release-sha", strings.Repeat("b", 40),
			"--server-url", server, "--repository", "group/source", "--auth-file", "auth.json",
			"--layouts-dir", "missing-native-layouts", "--output-dir", "never-published", "--prepublication-sboms",
		})
		require.Error(t, err)
		if strings.HasPrefix(server, "https:") {
			require.ErrorContains(t, err, "missing-native-layouts", "the inferred provider URL must pass URL admission before layout admission")
		} else {
			require.ErrorContains(t, err, "native SBOM URL must be public HTTPS")
		}
		require.NoDirExists(t, "never-published")
	}
}

// The plan file is the job verbs' transport: what plan --output writes,
// build and publish read back and admit against the same destination and tag.
func TestReleaseImagesPlanOutput_RoundTripsThroughTheJobVerbs(t *testing.T) {
	writeReleaseJobsArtifacts(t)

	require.NoError(t, releaseImagesPlanCmd().Run(t.Context(), releaseJobsPlanArgs("--output", "plan.json", "--runner-archs", "amd64,arm64")))

	var plan pipeline.ReleaseImagesPlan

	reader := &cli.Command{
		Name:  "read",
		Flags: releaseImagesJobFlags(),
		Action: func(_ context.Context, cmd *cli.Command) error {
			var err error

			plan, err = readReleaseImagesPlanFile(cmd)

			return err
		},
	}
	require.NoError(t, reader.Run(t.Context(), []string{"read", "--plan", "plan.json", "--image-repository", "registry.example/group/app", "--release-tag", "v1.2.3", "--release-sha", "abc"}))
	require.Len(t, plan.Builds, 2)
	require.Len(t, plan.Images, 1)
	require.Equal(t, "staging-v1.2.3", plan.Images[0].CandidateTag)

	// Another destination is refused: the plan is not trusted for it.
	err := reader.Run(t.Context(), []string{"read", "--plan", "plan.json", "--image-repository", "registry.example/other/app", "--release-tag", "v1.2.3", "--release-sha", "abc"})
	require.ErrorIs(t, err, errs.ErrInvalidConfig)
}

// A container whose architecture has no native build job stops at the plan,
// before any runner builds.
func TestReleaseImagesPlanOutput_RefusesAnArchitectureWithoutABuildJob(t *testing.T) {
	writeReleaseJobsArtifacts(t)

	err := releaseImagesPlanCmd().Run(t.Context(), releaseJobsPlanArgs("--output", "plan.json", "--runner-archs", "amd64"))
	require.ErrorIs(t, err, errs.ErrInvalidConfig)
	require.ErrorContains(t, err, "linux/arm64")
	require.NoFileExists(t, "plan.json")

	t.Setenv("RELEASE_PLATFORMS", "linux/amd64")
	require.NoError(t, releaseImagesPlanCmd().Run(t.Context(), releaseJobsPlanArgs("--output", "plan.json", "--runner-archs", "amd64")))

	err = releaseImagesPlanCmd().Run(t.Context(), releaseJobsPlanArgs("--runner-archs", "amd64"))
	require.ErrorIs(t, err, errs.ErrUsage)
}

func TestReleaseImagesResolvedNativePlansPreserveIdentityAndBasePolicy(t *testing.T) {
	writeReleaseJobsArtifacts(t)
	executable := filepath.Join(t.TempDir(), "executor")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nexit 99\n"), 0o700))
	tool := signerImageTool{&kaniko.Adapter{Executor: executable}}
	identity, err := tool.Identity()
	require.NoError(t, err)
	for _, base := range []bool{true, false} {
		var plan pipeline.ReleaseImagesPlan
		// Start with the same artifacts planner used by ordinary native jobs.
		require.NoError(t, releaseImagesPlanCmd().Run(t.Context(), releaseJobsPlanArgs("--output", "ordinary.json")))
		body, err := os.ReadFile("ordinary.json")
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &plan))
		if base {
			image := &plan.Images[0]
			image.ImageKind, image.Role, image.Flavor = "base", "base", "core"
			image.BaseInputID = strings.Repeat("a", 64)
			image.FinalTagName = image.BaseInputID + "-core"
			image.CandidateTag = "staging-" + image.FinalTagName
			image.Revision = image.BaseInputID
			image.RequiredLabels = "org.example.base-input-id=" + image.BaseInputID
			image.CacheTagPrefix = "cache-" + image.FinalTagName
			for i := range plan.Builds {
				build := &plan.Builds[i]
				build.FinalTagName, build.CandidateTag, build.Revision = image.FinalTagName, image.CandidateTag, image.Revision
				build.RequiredLabels = image.RequiredLabels
				build.Labels = image.RequiredLabels
				build.CacheTag = image.CacheTagPrefix + "-" + build.Arch
			}
		}
		for i := range plan.Builds {
			plan.Builds[i].BuilderMaterialID = identity.MaterialID()
		}
		if base {
			require.ErrorIs(t, writeReleaseImagesPlanFile("refused-base.json", plan, "amd64,arm64"), errs.ErrInvalidConfig)
			require.NoFileExists(t, "refused-base.json")
			// Domain transport still admits base plans for the supported base
			// lifecycle. A planted plan cannot bypass native job admission.
			body, err = json.Marshal(plan)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile("resolved.json", body, 0o600))
		} else {
			require.NoError(t, writeReleaseImagesPlanFile("resolved.json", plan, "amd64,arm64"))
		}
		body, err = os.ReadFile("resolved.json")
		require.NoError(t, err)
		admitted, err := pipeline.ReadImageBuildPlan(string(body), "registry.example/group/app", "v1.2.3")
		require.NoError(t, err)
		require.Equal(t, plan, admitted)
		input := releaseImageBuildInputFor(t, admitted.Builds[0])
		require.Equal(t, identity.MaterialID(), input.BuilderMaterialID)
		if base {
			require.Equal(t, admitted.Images[0].Revision, input.SourceSHA)
			require.Equal(t, admitted.Builds[0].CacheTag, input.CacheTag)
			require.Equal(t, []string{admitted.Images[0].RequiredLabels}, input.CacheLabels)
			require.Contains(t, input.Labels, admitted.Images[0].RequiredLabels)
		}
		require.NoError(t, appcontainer.CheckSignerImageBuilderIdentity(tool, input.BuilderMaterialID))
		// Hash the real adapter executable again: no build/probe may run.
		require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nexit 98\n"), 0o700))
		require.ErrorIs(t, appcontainer.CheckSignerImageBuilderIdentity(tool, input.BuilderMaterialID), errs.ErrValidation)
		err = releaseImagesBuildCmd().Run(t.Context(), []string{
			"build", "--plan", "resolved.json", "--arch", runtime.GOARCH,
			"--image-repository", "registry.example/group/app", "--release-tag", "v1.2.3",
			"--release-sha", strings.Repeat("b", 40), "--server-url", "https://gitlab.example",
			"--repository", "group/app", "--created", "2026-01-02T03:04:05Z",
			"--metadata-dir", "never-staged", "--image-builder", "kaniko", "--kaniko-executor", executable,
		})
		if base {
			require.ErrorIs(t, err, errs.ErrInvalidConfig)
			require.ErrorContains(t, err, "canonical premade-sboms handoff")
		} else {
			require.ErrorIs(t, err, errs.ErrValidation)
			require.ErrorContains(t, err, "native builder material ID differs")
		}
		require.NoDirExists(t, "never-staged")
		buildJSON, err := json.Marshal(admitted.Builds[0])
		require.NoError(t, err)
		err = releaseImagesBuildPreparedCmd().Run(t.Context(), []string{
			"build-prepared", "--build-json", string(buildJSON), "--inputs-dir", "never-inputs",
			"--release-tag", "v1.2.3", "--release-sha", strings.Repeat("b", 40),
			"--server-url", "https://gitlab.example", "--repository", "group/app",
			"--created", "2026-01-02T03:04:05Z", "--metadata-dir", "never-staged",
			"--image-builder", "kaniko", "--kaniko-executor", executable,
		})
		require.ErrorIs(t, err, errs.ErrValidation)
		require.ErrorContains(t, err, "native builder material ID differs")
		require.NoDirExists(t, "never-staged")
		require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nexit 99\n"), 0o700))
	}
}

// releaseImageBuildInputFor is the build input of build under the release
// job verbs' flags.
func releaseImageBuildInputFor(t *testing.T, build pipeline.ReleaseImageBuild) appcontainer.SignerImageBuildArchInput {
	t.Helper()

	var input appcontainer.SignerImageBuildArchInput

	cmd := &cli.Command{
		Name: "build",
		Flags: append(releaseImagesJobFlags(),
			&cli.StringFlag{Name: flagSourceDir, Value: "."},
			&cli.StringFlag{Name: flagCreated},
			&cli.StringFlag{Name: flagMetadataDir},
		),
		Action: func(_ context.Context, cmd *cli.Command) error {
			input = releaseImageBuildInput(cmd, build)

			return nil
		},
	}
	require.NoError(t, cmd.Run(t.Context(), []string{
		"build", "--plan", "p", "--image-repository", "r", "--release-tag", "v1.2.3", "--release-sha", "abc",
		"--server-url", "https://gitlab.example/", "--repository", "group/app", "--created", "2026-01-02T03:04:05Z",
		"--source-dir", "consumer", "--metadata-dir", "staged",
	}))

	return input
}

// Each planned build is the build-arch --stage call the Forgejo workflow's
// build step makes: the same tag, paths and labels.
func TestReleaseImageBuildInput_IsTheForgejoBuildStep(t *testing.T) {
	build := pipeline.ReleaseImageBuild{
		Name: "app", Arch: "amd64", ContainerFile: "Containerfile", Context: ".", Target: "runtime",
		BuildArgs: "A=1\nB=2", Labels: "org.opencontainers.image.licenses=MIT",
		ImageRepository: "registry.example/group/app", CandidateTag: "staging-v1.2.3", FinalTagName: "v1.2.3",
	}

	in := releaseImageBuildInputFor(t, build)
	require.Equal(t, "abc", in.SourceSHA)
	require.Equal(t, "staging-v1.2.3", in.TagName)
	require.Equal(t, "runtime", in.Target)
	require.Equal(t, filepath.Join("consumer", "Containerfile"), in.Containerfile)
	require.Equal(t, "consumer", in.Context)
	require.Equal(t, filepath.Join("staged", "app-arch-amd64"), in.MetadataDir)
	require.Equal(t, []string{"A=1", "B=2"}, in.BuildArgs)
	require.Equal(t, []string{
		"org.opencontainers.image.version=v1.2.3",
		"org.opencontainers.image.url=https://gitlab.example/group/app",
		"org.opencontainers.image.documentation=https://gitlab.example/group/app#readme",
		"org.opencontainers.image.ref.name=v1.2.3",
		"org.opencontainers.image.created=2026-01-02T03:04:05Z",
		"org.opencontainers.image.licenses=MIT",
	}, in.Labels)

	build.Revision = "def"
	in = releaseImageBuildInputFor(t, build)
	require.Equal(t, "def", in.SourceSHA)
}

// Each architecture's job builds that architecture's planned builds alone, in
// plan order; an architecture the plan does not build leaves its job no work.
func TestReleaseImageBuildsOf_IsTheArchitecturesShareOfThePlan(t *testing.T) {
	plan := pipeline.ReleaseImagesPlan{Builds: []pipeline.ReleaseImageBuild{
		{Name: "app", Arch: "amd64"}, {Name: "app", Arch: "arm64"}, {Name: "worker", Arch: "amd64"},
	}}

	require.Equal(t, []pipeline.ReleaseImageBuild{{Name: "app", Arch: "amd64"}, {Name: "worker", Arch: "amd64"}}, releaseImageBuildsOf(plan, "amd64"))
	require.Equal(t, []pipeline.ReleaseImageBuild{{Name: "app", Arch: "arm64"}}, releaseImageBuildsOf(plan, "arm64"))

	plan.Builds = plan.Builds[:1]
	require.Empty(t, releaseImageBuildsOf(plan, "arm64"))
}
