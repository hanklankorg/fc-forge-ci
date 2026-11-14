// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	gitlabapi "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v3"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/app/gitlabpipeline"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
	"github.com/diggsweden/forge-ci/internal/retry"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

func TestGitLabRelease_MinimalSignedArtifacts(t *testing.T) {
	registerLaneScenario(t)
	runGitLabRelease(t, "")
}

func TestGitLabRelease_ConsumerTrial(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	// A full run keeps its coverage requirement: only a focused one may skip.
	if os.Getenv(consumerCheckoutEnv) == "" && os.Getenv(livetest.PreparedEnv) == "" {
		livetest.NotCoveredf(t, "use the GitLab consumer-release recipe with a clean reviewed checkout")
	}

	forEachConsumerCheckout(t, provider.ForgeGitLab, "just test-live-consumer gitlab <checkout> release job-archive=<path> build-archive=<path> signer-archive=<path>", func(t *testing.T, root string) {
		t.Helper()
		runGitLabRelease(t, root)
	}, livetest.TrialRelease)
}

// Exercise first-push protection without publishing tool images or running a
// release. GitLab can initialize its default rule after the Git push returns.
func TestGitLabRelease_ConsumerBranchProtection(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	forScenarioConsumer(t, provider.ForgeGitLab, "consumer branch protection requires a clean reviewed checkout or a prepared directory", func(t *testing.T, root string) {
		t.Helper()

		for _, visibility := range livetest.ConsumerVisibilities(t) {
			t.Run(visibility.Name, func(t *testing.T) {
				runGitLabBranchProtection(t, root, visibility.Private)
			})
		}
	})
}

func TestGitLabRelease_MinimalBranchProtection(t *testing.T) {
	registerLaneScenario(t)

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabBranchProtection(t, "", visibility.Private)
		})
	}
}

func runGitLabBranchProtection(t *testing.T, root string, private bool) {
	t.Helper()

	target := livetest.Accept(t, provider.ForgeGitLab)

	var consumer *livetest.Consumer

	objectFormat := "sha1"

	if root != "" {
		opened := livetest.OpenConsumer(t, target, root)
		consumer, objectFormat = &opened, opened.ObjectFormat
	}

	repo := livetest.NewBareScratchRepo(t, target, "consumer-branch-protection", objectFormat, private)
	_, public := livetest.SSHKey(t, t.TempDir())

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	deploy, _, err := client.DeployKeys.AddDeployKey(livetest.RepoSlug(target, repo), &gitlabapi.AddDeployKeyOptions{
		Title: new("fixture release signer"), Key: new(public), CanPush: new(true),
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	livetest.RestrictGitLabReleaseBranch(t, target, repo, deploy.ID)

	if consumer != nil {
		consumer.PushSnapshot(t, repo)
	} else {
		seedGitLabReleaseCanary(t, ctx, client, livetest.RepoSlug(target, repo), map[string]string{
			"README.md": "native release canary\n", "second-file.txt": "same initial commit\n",
		})
	}

	branch, _, err := client.ProtectedBranches.GetProtectedBranch(livetest.RepoSlug(target, repo), "main", gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if branch == nil || branch.AllowForcePush || len(branch.PushAccessLevels) != 1 || branch.PushAccessLevels[0] == nil ||
		branch.PushAccessLevels[0].DeployKeyID != deploy.ID || branch.PushAccessLevels[0].UserID != 0 || branch.PushAccessLevels[0].GroupID != 0 {
		t.Fatal("the first push changed the release branch's configured protection")
	}
}

// Check a caller-prepared builder in the actual runner before paying for the
// consumer build matrix. Requires the same guarded full-release profile.
func TestGitLabRelease_BuildImageCapability(t *testing.T) {
	registerLaneScenario(t)

	// A prepared directory's Buildah job image is such a builder
	// (registerLaneScenario).
	archive := os.Getenv("FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE")
	if archive == "" || !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "builder probe requires a reviewed build image archive, or a prepared directory's Buildah job image, and a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	label := buildahRunner(t, target, "this probe checks the build image's own Buildah on the privileged runner")
	repo := livetest.NewPublicScratchRepo(t, target, "build-image-probe", livetest.ReadProbeAssetAnonymously)
	image := livetest.PushJobImage(t, target, repo, archive)

	const tag = "v0.0.9-build-probe"
	livetest.PrepareTag(t, target, repo, tag)
	livetest.PublishBinaryAsset(t, target, repo, tag, t.TempDir())
	prelude := livetest.ProbePrelude(target, livetest.ReleaseAssetURL(t, target, repo, tag, "forge-ci"))

	body, err := yaml.Marshal(map[string]any{"probe": map[string]any{
		// As release-jobs.yml declares a build image: its own entrypoint is not the job's shell.
		"image":     map[string]any{"name": image, "entrypoint": []string{""}},
		"tags":      []string{label},
		"variables": map[string]string{"GIT_STRATEGY": "empty"},
		"script":    []string{"set -euo pipefail", prelude, gitlabBuildImageProbe},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if conclusion := livetest.RunWorkflow(t, target, repo, "build-image-probe", string(body)); conclusion != "success" {
		t.Fatalf("the prepared builder cannot complete its isolated RUN probe: %s", conclusion)
	}
}

const gitlabBuildImageProbe = `work=$(mktemp -d /tmp/build-image-probe.XXXXXXXX)
trap 'rm -rf -- "${work}"' EXIT
export CONTAINERS_STORAGE_CONF="${work}/storage.conf" TMPDIR="${work}/tmp" CI_OUTPUT="${work}/outputs"
./forge-ci container setup-buildah --install-packages=false \
  --temp-dir "${work}" --storage-conf "${CONTAINERS_STORAGE_CONF}" \
  --storage-root "${work}/storage" --container-tmpdir "${TMPDIR}" --env-file "${work}/runtime.env"
mkdir "${work}/context"
cp forge-ci "${work}/context/probe"
printf 'FROM scratch\nCOPY probe /probe\nRUN ["/probe", "--version"]\n' > "${work}/context/Containerfile"
buildah build --isolation chroot --network host --pull=never \
  --tag localhost/fci-build-image-probe "${work}/context"
`

func runGitLabRelease(t *testing.T, consumerRoot string) {
	t.Helper()
	runGitLabReleaseScenario(t, consumerRoot, gitlabStandardRelease)
}

type gitlabReleaseCase uint8

const (
	gitlabStandardRelease gitlabReleaseCase = iota
	gitlabInterruptedConsumerRelease
	gitlabPreseededProvenanceRelease
)

// gitlabConsumerSettings returns the caller's settings for a release trial.
func gitlabConsumerSettings(t *testing.T, consumerRoot string) livetest.GitLabReleaseSettings {
	t.Helper()

	settings, err := livetest.ParseGitLabReleaseSettings(os.Getenv(consumerSettingsEnv))
	if err != nil {
		t.Fatal(err)
	}

	if consumerRoot == "" && (len(settings.Variables) != 0 || settings.Version != "" || settings.NextVersion != "" || settings.Fixtures != "") {
		t.Fatal("consumer settings require a consumer release trial")
	}

	return settings
}

// gitlabConsumerProfile is what a GitLab consumer's release and lifecycle
// derive from the checkout and the settings before anything is created: the
// build command, the selected builder, the release images the consumer
// declares, its export commands, the tags of the requested version, whether
// the release plan bumps a root npm package, and the npm packages the plan
// sends to the project's package registry.
type gitlabConsumerProfile struct {
	build                string
	kaniko               bool
	releaseImages        bool
	releaseImagesConfig  string
	export               []string
	requestTag, finalTag string
	// changelog are the renderer inputs the consumer's configuration calls
	// for; empty leaves the component's default.
	changelog map[string]any
	npmBump   bool
	packages  []string
	// baseStages and basePlan are the base image graph the consumer ships
	// for its policy, and the planner the policy runs for a stage.
	baseStages, basePlan string
}

// gitlabConsumerProfileOf binds the settings to the lab's runners and reads
// the consumer's build, release images and export. Only a consumer that
// declares release images builds them, in the release's image jobs with the
// selected builder. A container build belongs to the export jobs, which
// build with the selected builder; the build job has none to select, so a
// build script that runs one is refused.
func gitlabConsumerProfileOf(t *testing.T, consumerRoot string, settings livetest.GitLabReleaseSettings) (livetest.GitLabReleaseSettings, gitlabConsumerProfile) {
	t.Helper()

	// A consumer's release builds for the lab's runners, and only for them.
	if consumerRoot != "" {
		arch, err := livetest.LabRunnerArch()
		if err != nil {
			t.Fatal(err)
		}

		if settings.Variables, err = livetest.NativeReleasePlatforms(settings.Variables, arch); err != nil {
			t.Fatal(err)
		}
	}

	profile := gitlabConsumerProfile{kaniko: livetest.GitLabKaniko(t), requestTag: releaseRequestTag, finalTag: releaseFinalTag}

	var err error
	if profile.build, err = livetest.GitLabConsumerBuildCommand(settings.Variables); err != nil {
		t.Fatal(err)
	}

	if profile.releaseImages, profile.releaseImagesConfig, err = livetest.GitLabReleaseImages(consumerRoot); err != nil {
		t.Fatal(err)
	}

	if consumerRoot != "" && gitlabConsumerBuildRunsAContainerBuild(t, consumerRoot) {
		t.Fatalf("%s runs a container build, which the build job would run with Buildah whatever image-builder says; move it to %s", livetest.GitLabConsumerBuildScript, livetest.GitLabConsumerExportScript)
	}

	if profile.export, err = livetest.GitLabConsumerExportCommands(consumerRoot, settings.Variables); err != nil {
		t.Fatal(err)
	}

	if profile.changelog, err = livetest.GitLabChangelogInputs(consumerRoot); err != nil {
		t.Fatal(err)
	}

	if profile.baseStages, profile.basePlan, err = livetest.GitLabBaseStages(consumerRoot); err != nil {
		t.Fatal(err)
	}

	if settings.Version != "" {
		profile.requestTag, profile.finalTag = "release-request/v"+settings.Version, "v"+settings.Version
	}

	bumps, err := livetest.PlannedVersionBumps(consumerRoot, profile.artifactsConfig())
	if err != nil {
		t.Fatal(err)
	}

	profile.npmBump = slices.ContainsFunc(bumps, func(item pipeline.PlannedArtifact) bool {
		return item.ProjectType == projecttype.NPM && path.Clean(item.WorkingDirectory) == "."
	})

	if consumerRoot != "" {
		if profile.packages, err = livetest.PlannedNPMPackages(consumerRoot, profile.artifactsConfig()); err != nil {
			t.Fatal(err)
		}
	}

	return settings, profile
}

// awaitGitLabPackages refuses, before the release is requested, a project
// whose package registry already holds a planned package at the release's
// version: finding it afterwards would then show nothing. The registry is
// the project's and goes with it; the version is removed when the test ends
// all the same, and a kept demo keeps its package.
func awaitGitLabPackages(t *testing.T, target livetest.Target, repo string, packages []string, finalTag string) {
	t.Helper()

	version := strings.TrimPrefix(finalTag, "v")

	for _, name := range packages {
		if listed := livetest.PublishedPackageVersions(t, target, repo, "npm", name); slices.Contains(listed, version) {
			t.Fatalf("the project's package registry already holds %s@%s, which a release cannot publish again", name, version)
		}

		if livetest.DemoName() == "" {
			t.Cleanup(func() { livetest.DeletePublishedPackage(t, target, repo, "npm", name, version) })
		}
	}
}

// requireGitLabPackages holds a successful release to every npm package its
// plan sends to forge-packages: the project's package registry lists each at
// the released version.
func requireGitLabPackages(t *testing.T, target livetest.Target, repo string, packages []string, finalTag string) {
	t.Helper()

	version := strings.TrimPrefix(finalTag, "v")

	for _, name := range packages {
		if listed := livetest.PublishedPackageVersions(t, target, repo, "npm", name); !slices.Contains(listed, version) {
			t.Errorf("the release succeeded, but the project's package registry lists %v of %s and not %s", listed, name, version)
		}
	}
}

// artifactsConfig is the consumer's release images declaration, when it
// declares any.
func (profile gitlabConsumerProfile) artifactsConfig() string {
	if profile.releaseImages {
		return profile.releaseImagesConfig
	}

	return ""
}

// gitlabImageBuilder names the builder the pushed images select and its job
// image: Buildah, unless the run selected kaniko.
func gitlabImageBuilder(images gitlabReleaseImages) (string, string) {
	if images.kaniko != "" {
		return livetest.ImageBuilderKaniko, images.kaniko
	}

	return livetest.ImageBuilderBuildah, images.buildah
}

// protectGitLabReleaseTags lets maintainers push release requests and lets
// only maintainers and the signer's deploy key create version tags.
func protectGitLabReleaseTags(ctx context.Context, t *testing.T, client *gitlabapi.Client, project string, deployKeyID int64) {
	t.Helper()

	if _, _, err := client.ProtectedTags.ProtectRepositoryTags(project, &gitlabapi.ProtectRepositoryTagsOptions{
		Name: new("release-request/*"), CreateAccessLevel: gitlabapi.Ptr(gitlabapi.MaintainerPermissions),
	}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := client.ProtectedTags.ProtectRepositoryTags(project, &gitlabapi.ProtectRepositoryTagsOptions{
		Name: new("v*"), AllowedToCreate: new([]*gitlabapi.TagsPermissionOptions{
			{AccessLevel: gitlabapi.Ptr(gitlabapi.MaintainerPermissions)}, {DeployKeyID: new(deployKeyID)},
		}),
	}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
}

// useGitLabAdminPolicy points the consumer's pipeline at the pinned policy
// file and takes pipeline variables and job-token pushes away from everyone.
//
// The policy is not resolved here. GitLab's project lint, GET
// /projects/:id/ci/lint, reads the configuration from the project's own
// repository at ci_config_path, so a policy in another project,
// file@project:ref, is to it a file the project does not have: it answers
// 404 for an empty project and "Please provide content of .gitlab-ci.yml"
// for one with commits. A policy GitLab refuses is read from the pipeline it
// records instead, whose yaml_errors GitLabPipelineAccount reports.
func useGitLabAdminPolicy(ctx context.Context, t *testing.T, client *gitlabapi.Client, project, configPath string) {
	t.Helper()

	if _, _, err := client.Projects.EditProject(project, &gitlabapi.EditProjectOptions{
		CIConfigPath:                           new(configPath),
		CIPipelineVariablesMinimumOverrideRole: gitlabapi.Ptr(gitlabapi.CIPipelineVariablesMinimumOverrideRoleValue("no_one_allowed")),
		CIPushRepositoryForJobTokenAllowed:     new(false),
	}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
}

// gitlabReleaseRow is what the visibilities of one release row share: what
// the checkout and the settings say before anything is created, and the job
// images pushed for the row.
type gitlabReleaseRow struct {
	consumerRoot string
	scenario     gitlabReleaseCase
	settings     livetest.GitLabReleaseSettings
	profile      gitlabConsumerProfile
	archives     gitlabReleaseArchives
	images       gitlabReleaseImages
	pushed       livetest.JobImages
}

// runGitLabReleaseScenario runs the release once for each visibility. The
// job images are pushed once, before them: no run changes them, and every
// visibility reads them from public projects. Each visibility serves a layer
// of its own, and pushes beside it the kaniko job image, which is labelled
// with that layer.
func runGitLabReleaseScenario(t *testing.T, consumerRoot string, scenario gitlabReleaseCase) {
	t.Helper()

	visibilities := livetest.ConsumerVisibilities(t)

	settings, profile := gitlabConsumerProfileOf(t, consumerRoot, gitlabConsumerSettings(t, consumerRoot))
	bases := profile.baseStages != ""

	recovery := scenario == gitlabInterruptedConsumerRelease

	preseeded := scenario == gitlabPreseededProvenanceRelease
	if preseeded && (consumerRoot == "" || settings.NextVersion != "") {
		t.Fatal("preseeded provenance requires a consumer checkout and no next-version")
	}

	if recovery && (consumerRoot == "" || settings.Version == "" || settings.Version == "0.0.0" || settings.NextVersion != "") {
		t.Fatal("consumer recovery requires one explicit nonzero version and no next-version")
	}

	if recovery && !profile.releaseImages {
		t.Fatal("consumer recovery requires policy-owned release-images")
	}

	// The export jobs, like the image jobs, run in the builder's job image.
	archives := gitlabReleaseArchiveInputs(t, consumerRoot != "", profile.releaseImages || len(profile.export) != 0 || bases, profile.kaniko)
	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "signed GitLab release requires a deployed runner")
	}

	requireGitLabDemoScenario(t, consumerRoot)

	target := livetest.Accept(t, provider.ForgeGitLab)
	pushed := livetest.JobImages{}
	row := gitlabReleaseRow{
		consumerRoot: consumerRoot, scenario: scenario, settings: settings, profile: profile,
		archives: archives, images: pushGitLabReleaseImages(t, target, archives, pushed), pushed: pushed,
	}

	for _, visibility := range visibilities {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabReleaseVisibility(t, row, visibility.Private)
		})
	}
}

func runGitLabReleaseVisibility(t *testing.T, row gitlabReleaseRow, private bool) {
	t.Helper()

	consumerRoot, settings, profile := row.consumerRoot, row.settings, row.profile
	bases := profile.baseStages != ""
	recovery := row.scenario == gitlabInterruptedConsumerRelease
	preseeded := row.scenario == gitlabPreseededProvenanceRelease
	requestTag, finalTag := profile.requestTag, profile.finalTag

	target := livetest.Accept(t, provider.ForgeGitLab)

	var consumer *livetest.Consumer

	gpgFiles := false

	if consumerRoot != "" {
		opened := livetest.OpenConsumer(t, target, consumerRoot)
		opened, gpgFiles = stageGitLabFixtures(t, opened, consumerRoot, settings, profile)

		if preseeded {
			opened = opened.WithPreseededGitLabProvenance(t)
		}

		if livetest.DemoName() != "" {
			opened = opened.WithHistory()
		}

		consumer = &opened
	}

	layer := livetest.PushLayer(t, target, gitlabReleaseRepo(t, target, "release-engine", livetest.ReadLayerAnonymously))
	if consumer != nil {
		*consumer = consumer.WithDraftLayer(t, layer)
	}

	images := row.images
	images.kaniko = pushGitLabKanikoJobImage(t, target, row.archives, layer)
	jobImage := images.job
	publication := gitlabReleaseRepo(t, target, "release-publication", "")
	policyRepo := gitlabReleaseRepo(t, target, "release-owned-policy", "")

	objectFormat := "sha1"
	if consumer != nil {
		objectFormat = consumer.ObjectFormat
	}

	repo := gitlabReleaseConsumerRepo(t, target, objectFormat, private)
	project := livetest.RepoSlug(target, repo)

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(livetest.Context(t), 75*time.Minute)
	defer cancel()

	keys := newReleaseKeys(t)
	livetest.RetainGitLabJobLogsAfter(t, target, repo, map[string]string{
		"ssh": keys.sshPrivate, "cosign": keys.cosignPrivate, "passphrase": keys.passphrase,
	})
	requestKeyDir := t.TempDir()
	_, requestPublic := livetest.SSHKey(t, requestKeyDir)

	gitURL, hostKey, supported := livetest.SSHRoute(t, target, repo)
	if !supported {
		t.Fatal("the selected GitLab endpoint needs a pinned runner-side SSH route")
	}

	deploy, _, err := client.DeployKeys.AddDeployKey(project, &gitlabapi.AddDeployKeyOptions{
		Title: new("fixture release signer"), Key: new(keys.sshPublic), CanPush: new(true),
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	protectGitLabReleaseTags(ctx, t, client, project, deploy.ID)

	// A kept demo holds no key or token once the run is over.
	if livetest.DemoName() != "" {
		livetest.DeleteSecretsAfter(t, target, repo, gitlabReleaseSecrets)
	}

	keys.gpgFingerprint = configureGitLabReleaseVariables(t, target, client, project, keys, consumer != nil)

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	checks, build := []string{"go test ./..."}, []string{gitlabMinimalBuild}
	if consumer != nil {
		checks = []string{"bash .gitlab/check-release.sh"}
		build = []string{profile.build}
	}

	// The consumer's checks and build commands, every planned family job, the
	// final release-files invocation and the image and export jobs run on
	// build-runner-tags-linux-amd64, which is never empty: the contract's
	// build pool, whichever builder runs, since GitLab's ordinary pool takes
	// untagged jobs and has no tag to give. A lab that cannot run the
	// selected builder reports the scenario not covered.
	if profile.releaseImages || len(profile.export) != 0 || bases {
		livetest.ChooseImageBuilder(t, target)
	}

	buildLabel := livetest.PrivilegedRunnerLabel(t, target)
	if buildLabel == "" {
		livetest.NotCoveredf(t, "a GitLab release's build jobs need a tag, and the contract publishes no build runner pool to take it from")
	}

	buildRunnerTags := []string{buildLabel}

	runnerTags := []string{}

	imageBuilder, builderJobImage := gitlabImageBuilder(images)

	// The lab's runners are of one architecture, and only amd64 has an image
	// or export job without tags of its own.
	releasePlatforms := ""
	if profile.releaseImages || len(profile.export) != 0 {
		releasePlatforms = settings.Variables["RELEASE_PLATFORMS"]
		if releasePlatforms != "linux/amd64" {
			livetest.NotCoveredf(t, "the GitLab release image and export jobs build %s only on tagged arm64 runners, which the lab has none of", releasePlatforms)
		}
	}

	// The further job images of the consumer's gate, each the runtime image of
	// the toolchain family it is named for, which that family's release jobs
	// run in too.
	gate := consumer != nil && consumer.HasGitLabGate(t)

	gateImages := livetest.GitLabGateImages{Job: jobImage, Lint: images.lint}
	if gate {
		gateImages.ExtraJobs = livetest.PushGitLabGateExtraJobImages(t, target, *consumer, maps.Clone(row.pushed), func(name string) string {
			return gitlabReleaseRepo(t, target, "release-job-image-"+strings.ToLower(name), livetest.ReadJobImageAnonymously)
		})
	}

	releasePolicy := gitlabReleasePolicy{
		project: project, publication: livetest.RepoSlug(target, publication), registry: registry,
		images: images, familyImages: gateImages.ExtraJobs, keys: keys, gitURL: gitURL, hostKey: hostKey, requestPublic: requestPublic,
		settings: settings, distro: consumer != nil, baseStages: profile.baseStages, basePlan: profile.basePlan,
		checks: checks, runnerTags: runnerTags, build: build, buildRunnerTags: buildRunnerTags,
		releaseImages: profile.releaseImages, artifactsConfig: profile.artifactsConfig(), releasePlatforms: releasePlatforms,
		imageBuilder: imageBuilder, builderJobImage: builderJobImage, export: profile.export, changelog: profile.changelog,
	}

	policyIncludes := []any{releasePolicy.include(t, target, layer)}
	if recovery {
		// Extend the exact captured parent, not a retained fork or consumer CI.
		policyProject := livetest.RepoSlug(target, policyRepo)

		extension, err := livetest.GitLabRecoveryExtension(jobImage)
		if err != nil {
			t.Fatal(err)
		}

		extensionCommit := gitlabPolicyCommit(t, ctx, client, policyProject, "recovery-checkpoint.yml", string(extension), gitlabapi.FileCreate)

		parentSource, _, err := client.RepositoryFiles.GetRawFile(layer.Repository, "templates/release.yml", &gitlabapi.GetRawFileOptions{Ref: new(layer.Commit)}, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}

		wrapper, err := livetest.GitLabRecoveryParent(parentSource, layer.Commit, policyProject, extensionCommit.ID)
		if err != nil {
			t.Fatal(err)
		}

		wrapperCommit := gitlabPolicyCommit(t, ctx, client, policyProject, "templates/recovery.yml", string(wrapper), gitlabapi.FileCreate)
		policyIncludes[0].(map[string]any)["component"] = strings.TrimPrefix(target.BaseURL(), "https://") + "/" + policyProject + "/recovery@" + wrapperCommit.ID
	}
	// Snapshot the admitted MR gate into the independently pinned policy project.
	// Its jobs still check out the MR source; the consumer cannot rewrite the
	// administrator's component inputs. Release-request tags stay release-only.
	if gate {
		body := consumer.GitLabAdminPolicy(t, layer, gateImages)
		gateCommit := gitlabPolicyCommit(t, ctx, client, livetest.RepoSlug(target, policyRepo), livetest.GitLabAdminGateFile, body, gitlabapi.FileCreate)
		// The release is read for tag pipelines only, as docs/gitlab.md has
		// it: a gate that declares its own stages leaves the release job's
		// stage out, and GitLab refuses a merge-request pipeline that names it.
		policyIncludes[0].(map[string]any)["rules"] = []any{map[string]string{"if": "$CI_COMMIT_TAG"}}
		policyIncludes = append([]any{gitlabGateInclude(livetest.RepoSlug(target, policyRepo), gateCommit.ID)}, policyIncludes...)
	}

	policyConfig := map[string]any{"include": policyIncludes}

	policyBody, err := yaml.Marshal(policyConfig)
	if err != nil {
		t.Fatal(err)
	}

	policy := gitlabPolicyCommit(t, ctx, client, livetest.RepoSlug(target, policyRepo), "release.yml", string(policyBody), gitlabapi.FileCreate)
	useGitLabAdminPolicy(ctx, t, client, project, "release.yml@"+livetest.RepoSlug(target, policyRepo)+":"+policy.ID)

	livetest.RestrictGitLabJobToken(t, target, repo)

	files := map[string]string{
		"go.mod": "module example.invalid/layer-release-canary\n\ngo 1.27.4\n",
		"go.sum": "", "cmd/layer-release-canary/main.go": releaseCanaryMain,
		".chglog/config-keepachangelog.yml": chglogKeepConfig, ".chglog/CHANGELOG-keepachangelog.tpl": chglogKeepTemplate,
		".chglog/config-minimal.yml": chglogMinimalConfig, ".chglog/CHANGELOG-minimal.tpl": chglogMinimalTemplate,
		"CHANGELOG.md": releaseCanaryChangelog,
	}

	var request string
	// Protect every empty consumer before its first write. Seeding the minimal
	// fixture also needs one commit: later API writes cannot bypass protection.
	livetest.RestrictGitLabReleaseBranch(t, target, repo, deploy.ID)

	if consumer != nil {
		request = consumer.PushGitLabRelease(t, repo, layer, gateImages)
	} else {
		request = seedGitLabReleaseCanary(t, ctx, client, project, files)
	}

	requireObjectFormats(t, objectFormat, request, layer.Commit)

	var previousDigest string
	if recovery {
		previousDigest = livetest.PushImageTags(t, target, publication, "v0.0.0", "latest").Digest
		if _, _, err := client.ProjectVariables.CreateVariable(project, &gitlabapi.CreateProjectVariableOptions{
			Key: new(livetest.GitLabRecoveryPauseVariable), Value: new("true"), Protected: new(true),
		}, gitlabapi.WithContext(ctx)); err != nil {
			t.Fatal(err)
		}
	}

	awaitGitLabPackages(t, target, repo, profile.packages, finalTag)
	livetest.SignGitLabRequest(t, target, repo, request, requestTag, filepath.Join(requestKeyDir, "lab-ssh"))

	var (
		run   *gitlabapi.Pipeline
		child int64
	)
	if recovery {
		run, child = recoverGitLabConsumerAfterCandidate(t, ctx, client, target, repo, publication, requestTag, request, finalTag, previousDigest)
	} else {
		run = waitGitLabTagPipeline(t, ctx, client, project, requestTag, request)
		if preseeded {
			requireGitLabPreseededProvenanceRejection(t, ctx, client, project, livetest.RepoSlug(target, publication), run)

			return
		}

		child = requireGitLabReleaseChild(t, ctx, client, project, run)
	}

	if consumer != nil {
		logGitLabReleaseInventory(t, ctx, client, project, child)
	}

	requireReleaseGit(t, target, repo, keys, request, requestTag, finalTag)
	verified := requireGitLabReleaseBundles(t, client, target, repo, child, run.SHA, keys, finalTag, profile.releaseImages, gpgFiles)
	t.Logf("verified %d published signatures, GPG-signed files %t", verified, gpgFiles)

	// Read back and retain the public payloads before an independent routing
	// assertion can end the trial and run its disposable-resource cleanup.
	if run.SHA != request {
		t.Fatal("release routing plan must use the exact signed request commit")
	}

	plans := gitLabReleaseRunnerPlan(t, ctx, client, project, request, profile.artifactsConfig())

	expectedRunners, err := gitLabReleaseRunnerRoles(buildRunnerTags, runnerTags, profile.releaseImages, len(profile.export) != 0, profile.baseStages, plans)
	if err != nil {
		t.Fatal(err)
	}

	requireGitLabReleaseRunnerRouting(t, ctx, client, project, child,
		expectedRunners)

	if consumer != nil {
		requireGitLabPackageSignatures(t, target, repo, keys, finalTag)
		requireGitLabPublishedImages(t, client, target, repo, publication, child, keys)
		requireGitLabPackages(t, target, repo, profile.packages, finalTag)
	}

	t.Logf("GitLab release %s: parent %d, child %d, %d verified bundles", finalTag, run.ID, child, verified)

	// GitLab has no prerelease flag: the release says so in its notes.
	if settings.ReleaseOption == livetest.OptionPrerelease && !strings.HasPrefix(mustReadRelease(ctx, t, target, repo, finalTag).Body, "> **Pre-release.**") {
		t.Errorf("the release of %s, requested under release-policy stable-prerelease, does not open its notes with the pre-release notice", finalTag)
	}

	if profile.npmBump {
		requireNPMVersionContract(t, target, repo, finalTag)
	}

	// The base images the release promoted, for a later release to reuse.
	var promotedBases []string
	if bases {
		_, _, promotedBases = requireGitLabBases(t, ctx, client, target, project, publication, child, keys, profile.baseStages)
	}

	if settings.NextVersion != "" {
		first, before := finalTag, mustReadRelease(ctx, t, target, repo, finalTag)

		// Where each file's upload is served now, to ask again once the
		// file's link is gone.
		uploads := map[string]string{}

		if settings.ReleaseOption == livetest.OptionRetention {
			for _, name := range before.AssetNames() {
				uploads[name] = livetest.ReleaseAssetURL(t, target, repo, first, name)
			}
		}

		parent := mergeGitLabNextCandidate(t, client, target, repo)
		requestTag, finalTag := "release-request/v"+settings.NextVersion, "v"+settings.NextVersion
		awaitGitLabPackages(t, target, repo, profile.packages, finalTag)
		livetest.SignGitLabRequest(t, target, repo, parent, requestTag, filepath.Join(requestKeyDir, "lab-ssh"))
		next := waitGitLabTagPipeline(t, ctx, client, project, requestTag, parent)
		child := requireGitLabReleaseChild(t, ctx, client, project, next)
		requireReleaseGit(t, target, repo, keys, parent, requestTag, finalTag)

		bundles := requireGitLabReleaseBundles(t, client, target, repo, child, next.SHA, keys, finalTag, profile.releaseImages, gpgFiles)
		if consumer != nil {
			requireGitLabPackageSignatures(t, target, repo, keys, finalTag)
			requireGitLabPublishedImages(t, client, target, repo, publication, child, keys)
			requireGitLabPackages(t, target, repo, profile.packages, finalTag)
		}

		t.Logf("second consecutive GitLab release %s: parent commit %s, pipeline %d, child %d, %d verified bundles", finalTag, parent, next.ID, child, bundles)

		// The base graph is unchanged, so every stage the first release
		// promoted is reused: found promoted and verified, signed and promoted
		// no further, and handed on as the first release handed it on.
		if bases {
			stages, traces, reused := requireGitLabBases(t, ctx, client, target, project, publication, child, keys, profile.baseStages)
			if findings := livetest.BaseStageReuseFindings(stages, traces, promotedBases, reused); len(findings) != 0 {
				t.Fatalf("the second release %s did not reuse the base images %s promoted:\n%s", finalTag, first, strings.Join(findings, "\n"))
			}

			t.Logf("second release %s reused the %d verified base images of %s", finalTag, len(reused), first)
		}

		if settings.ReleaseOption == livetest.OptionRetention {
			after := mustReadRelease(ctx, t, target, repo, first)
			for _, finding := range livetest.ReleaseRetentionFindings(before.AssetNames(), after.AssetNames(), mustReadRelease(ctx, t, target, repo, finalTag).AssetNames()) {
				t.Error(finding)
			}

			// The links are half of it: a kept file serves the bytes it
			// served, and a removed file's upload is gone with its link.
			for _, original := range before.Assets {
				kept, linked := after.Asset(original.Name)
				if linked {
					if kept != original {
						t.Errorf("retention changed the served bytes of %s/%s", first, original.Name)
					}

					continue
				}

				if _, served := livetest.DownloadIfPresent(t, target, uploads[original.Name], 256<<20); served {
					t.Errorf("retention removed the link of %s/%s and left its upload", first, original.Name)
				}
			}

			t.Logf("retention: %s keeps %d of %d files", first, len(after.Assets), len(before.Assets))
		}

		if profile.npmBump {
			requireNPMVersionContract(t, target, repo, finalTag)
		}
	}

	if gate && !recovery {
		requireGitLabPolicyMergeRequest(t, client, project, repo, *consumer, request)
	}
}

// Prepared release commits carry [skip ci]. The next request must name a new
// candidate, admitted through normal MR merge authority rather than unprotecting
// main or changing the release bot's generated message.
func mergeGitLabNextCandidate(t *testing.T, client *gitlabapi.Client, target livetest.Target, repo string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), 5*time.Minute)
	defer cancel()

	project := livetest.RepoSlug(target, repo)

	const branch = "next-release-candidate"

	commit, _, err := client.Commits.CreateCommit(project, &gitlabapi.CreateCommitOptions{
		Branch: gitlabapi.Ptr(branch), StartBranch: new("main"), CommitMessage: new("test: request next release"),
		Actions: []*gitlabapi.CommitActionOptions{{Action: gitlabapi.Ptr(gitlabapi.FileCreate), FilePath: new("next-release-request.txt"), Content: new("release inputs remain unchanged\n")}},
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	waitGitLabPush(t, ctx, client, project, branch, commit.ID)

	mr, err := livetest.CreateGitLabMergeRequest(ctx, client, project, &gitlabapi.CreateMergeRequestOptions{
		SourceBranch: gitlabapi.Ptr(branch), TargetBranch: new("main"), Title: new("Request next release candidate"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = retry.Run(ctx, nil, 90, 2*time.Second, func() error {
		current, _, readErr := client.MergeRequests.GetMergeRequest(project, mr.IID, nil, gitlabapi.WithContext(ctx))
		if readErr != nil {
			return retry.Permanent(readErr)
		}

		if current.DetailedMergeStatus != "mergeable" {
			return fmt.Errorf("merge request is %s: %w", current.DetailedMergeStatus, errs.ErrDependencyUnavailable)
		}

		return nil
	}, retry.WithBackoff(retry.Constant)); err != nil {
		t.Fatal(err)
	}

	merged, _, err := client.MergeRequests.AcceptMergeRequest(project, mr.IID, &gitlabapi.AcceptMergeRequestOptions{
		SHA: new(commit.ID), MergeCommitMessage: new("test: merge next release candidate"), ShouldRemoveSourceBranch: new(true),
	}, gitlabapi.WithContext(ctx))
	if err != nil || merged.MergeCommitSHA == "" {
		t.Fatalf("merge next release candidate: %v", err)
	}

	return merged.MergeCommitSHA
}

// requireGitLabPolicyMergeRequest opens a merge request into the main the
// release left and requires the consumer's gate, admitted by the policy for
// merge-request pipelines, to pass on it. The change is one commit on pushed,
// the commit whose tree the trial staged: on the released main it would carry
// that tree and undo the release commit.
func requireGitLabPolicyMergeRequest(t *testing.T, client *gitlabapi.Client, project, repo string, consumer livetest.Consumer, pushed string) {
	t.Helper()

	head := consumer.PushChange(t, repo, trialBranch, pushed)

	ctx, cancel := context.WithTimeout(livetest.Context(t), 30*time.Minute)
	defer cancel()

	waitGitLabPush(t, ctx, client, project, trialBranch, head)

	mr, err := livetest.CreateGitLabMergeRequest(ctx, client, project, &gitlabapi.CreateMergeRequestOptions{
		SourceBranch: new(trialBranch), TargetBranch: new("main"), Title: new("test: consumer trial"),
	})
	if err != nil {
		t.Fatal(err)
	}

	pipeline := waitGitLabMRPipeline(t, ctx, client, project, mr.IID, head, 0)
	t.Logf("GitLab MR !%d at %s under the release policy: pipeline %d ended %s", mr.IID, head, pipeline.ID, pipeline.Status)

	if pipeline.Status != "success" {
		logGitLabTrace(t, gitlabTrialTrace(t, ctx, client, project, pipeline.ID))
		livetest.FailOnRunConclusionf(t, "the consumer's gate under the release policy ended %s", pipeline.Status)
	}
}

// gitlabReleaseArchives are the prepared image archives of a release's jobs;
// build is empty when the release builds with the job image, lint when the
// consumer's merge-request gate has no nanolinter component.
type gitlabReleaseArchives struct{ job, build, buildah, kaniko, signer, lint string }

// gitlabConsumerBuildRunsAContainerBuild reports whether the consumer's own
// release build runs a container build, which belongs to its export.
func gitlabConsumerBuildRunsAContainerBuild(t *testing.T, consumerRoot string) bool {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(consumerRoot, livetest.GitLabConsumerBuildScript))
	if os.IsNotExist(err) {
		return false
	}

	if err != nil {
		t.Fatal(err)
	}

	return strings.Contains(string(body), "forge-ci container build ")
}

// gitlabReleaseArchiveInputs reads the archives the recipe names, and reports
// the scenario not covered when one it needs is missing: with release images,
// the job image of the selected builder.
func gitlabReleaseArchiveInputs(t *testing.T, needBuild, builderJobs, kaniko bool) gitlabReleaseArchives {
	t.Helper()

	archives := gitlabReleaseArchives{
		job: os.Getenv("FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE"), signer: os.Getenv("FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE"),
		lint: os.Getenv("FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE"),
	}
	if archives.job == "" || archives.signer == "" {
		livetest.NotCoveredf(t, "signed GitLab release requires reviewed job and signer image archives")
	}

	if needBuild {
		archives.build = os.Getenv("FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE")
		if archives.build == "" {
			livetest.NotCoveredf(t, "consumer release requires a reviewed build image archive beside the non-root check image")
		}
	}

	switch {
	case builderJobs && kaniko:
		archives.kaniko = os.Getenv("FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE")
		if archives.kaniko == "" {
			livetest.NotCoveredf(t, "kaniko release images and exports require FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE, a saved kaniko job image")
		}
	case builderJobs:
		archives.buildah = os.Getenv("FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE")
		if archives.buildah == "" {
			livetest.NotCoveredf(t, "Buildah release images and exports require FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE, a saved Buildah job image")
		}
	}

	return archives
}

// gitlabReleaseImages are the image references of a release's jobs.
type gitlabReleaseImages struct{ job, build, buildah, kaniko, signer, lint string }

// pushGitLabReleaseImages pushes each archive once, to a project named after
// the first role that takes it, and returns the image references. The kaniko
// job image is pushed beside the layer it runs from
// (pushGitLabKanikoJobImage).
func pushGitLabReleaseImages(t *testing.T, target livetest.Target, archives gitlabReleaseArchives, pushed livetest.JobImages) gitlabReleaseImages {
	t.Helper()

	push := func(scenario, archive string) string {
		if archive == "" {
			return ""
		}

		return pushed.Push(t, target, archive, func() string {
			return gitlabReleaseRepo(t, target, scenario, livetest.ReadJobImageAnonymously)
		})
	}

	images := gitlabReleaseImages{job: push("release-job-image", archives.job)}

	images.build = images.job

	if archives.build != "" {
		images.build = push("release-build-image", archives.build)
	}

	images.buildah = push("release-buildah-job-image", archives.buildah)
	images.signer = push("release-signer-image", archives.signer)
	images.lint = push("release-lint-image", archives.lint)

	return images
}

// pushGitLabKanikoJobImage pushes the kaniko job image, when the release has
// one, labelled with the layer it runs from, which admits it.
func pushGitLabKanikoJobImage(t *testing.T, target livetest.Target, archives gitlabReleaseArchives, layer livetest.Layer) string {
	t.Helper()

	if archives.kaniko == "" {
		return ""
	}

	return livetest.PushKanikoJobImage(t, target, gitlabReleaseRepo(t, target, "release-kaniko-job-image", livetest.ReadJobImageAnonymously), archives.kaniko, layer)
}

// requireGitLabDemoScenario refuses demo mode for anything but a consumer's
// release.
func requireGitLabDemoScenario(t *testing.T, consumerRoot string) {
	t.Helper()

	if livetest.DemoName() != "" && consumerRoot == "" {
		t.Fatal("demo mode shows a consumer's release; use `just demo gitlab <name>`")
	}
}

// gitlabComponent is the include reference of one of the layer's components
// at the layer's commit.
func gitlabComponent(target livetest.Target, layer livetest.Layer, name string) string {
	return strings.TrimPrefix(target.BaseURL(), "https://") + "/" + layer.Repository + "/" + name + "@" + layer.Commit
}

// gitlabReleasePolicy is what the harness writes into the release
// component's include: the projects of the release, its keys and routes, the
// caller's settings, and the jobs the consumer's release runs.
type gitlabReleasePolicy struct {
	project, publication, registry string
	images                         gitlabReleaseImages
	// familyImages are the runtime images of the toolchain families that do
	// not run in the build image, by family in capitals: the further job
	// images of the consumer's gate.
	familyImages                   map[string]string
	keys                           releaseKeys
	gitURL, hostKey, requestPublic string
	settings                       livetest.GitLabReleaseSettings
	// distro signs distro packages, a consumer's release.
	distro bool
	// baseStages and basePlan are the consumer's base image graph and its
	// planner, when it ships one.
	baseStages, basePlan              string
	checks, build, export             []string
	runnerTags, buildRunnerTags       []string
	releaseImages                     bool
	artifactsConfig, releasePlatforms string
	imageBuilder, builderJobImage     string
	changelog                         map[string]any
}

// include is the release component's include with the policy's inputs.
func (policy gitlabReleasePolicy) include(t *testing.T, target livetest.Target, layer livetest.Layer) map[string]any {
	t.Helper()

	inputs := map[string]any{
		"forge-ci-repository": layer.Repository, "consumer-project": policy.project,
		"server-url": target.BaseURL(), "registry": policy.registry,
		"job-image": policy.images.job, "job-user": policy.settings.JobUser, "signer-image": policy.images.signer,
		"publication-project":          policy.publication,
		"publication-image-repository": policy.registry + "/" + policy.publication,
		"cosign-public-key-sha256":     policy.keys.cosignPublicSHA256,
		"release-git-url":              policy.gitURL, "release-git-host-key-fingerprint": policy.hostKey,
		"request-allowed-signers": "livetest@localhost " + policy.requestPublic,
		"bot-name":                "Layer acceptance", "bot-email": releaseBotEmail,
		"sign-distro-packages":          policy.distro,
		"base-stages":                   policy.baseStages,
		"base-plan":                     policy.basePlan,
		"checks":                        policy.checks,
		"runner-tags":                   policy.runnerTags,
		"build":                         policy.build,
		"build-runner-tags-linux-amd64": policy.buildRunnerTags,
		"build-runner-tags-linux-arm64": []string{},
		"release-images":                policy.releaseImages,
		"artifacts-config":              policy.artifactsConfig,
		"release-platforms":             policy.releasePlatforms,
		"image-builder":                 policy.imageBuilder,
		"builder-job-image":             policy.builderJobImage,
		"export-files":                  len(policy.export) != 0,
		"export":                        policy.export,
	}
	// The consumer's build image serves each family the plan builds or bumps,
	// bar a family with a runtime image of its own.
	for _, family := range []string{"java", "node", "go", "rust", "android"} {
		inputs["runtime-image-"+family] = cmp.Or(policy.familyImages[strings.ToUpper(family)], policy.images.build)
	}

	maps.Copy(inputs, policy.changelog)
	maps.Copy(inputs, livetest.GitLabReleaseOptionInputs(policy.settings.ReleaseOption))

	return map[string]any{
		"component": gitlabComponent(target, layer, "release"),
		"inputs":    livetest.AdminInputs(t, inputs),
	}
}

// gitlabGateInclude admits the separately pinned administrator MR gate.
func gitlabGateInclude(project, commit string) map[string]any {
	return map[string]any{
		"project": project, "ref": commit, "file": livetest.GitLabAdminGateFile,
		"rules": []any{map[string]string{"if": `$CI_PIPELINE_SOURCE == "merge_request_event"`}},
	}
}

// gitlabReleaseSecrets are the release project's variables that hold a key, a
// passphrase or a token.
var gitlabReleaseSecrets = []string{
	"RELEASE_SSH_SIGNING_KEY", "RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_COSIGN_PASSPHRASE", "RELEASE_TOKEN",
	"RELEASE_REGISTRY_TOKEN", "RELEASE_PACKAGE_TOKEN", "RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_FINGERPRINT", "RELEASE_GPG_PASSPHRASE",
}

// gitlabReleaseRepo makes one of a release's helper projects: a scratch one a
// trial resets and deletes, or in demo mode a kept one named after the demo.
// A project the jobs read without credentials names why in public, and is
// public; any other follows the run's visibility.
func gitlabReleaseRepo(t *testing.T, target livetest.Target, scenario, public string) string {
	t.Helper()

	if demo := livetest.DemoName(); demo != "" {
		return livetest.NewDemoRepo(t, target, livetest.DemoHelperRepo(demo, strings.TrimPrefix(scenario, "release-")), "sha1", true)
	}

	if public != "" {
		return livetest.NewPublicScratchRepo(t, target, scenario, public)
	}

	return livetest.NewScratchRepo(t, target, scenario)
}

// gitlabReleaseConsumerRepo makes the empty project the consumer is pushed to.
func gitlabReleaseConsumerRepo(t *testing.T, target livetest.Target, objectFormat string, private bool) string {
	t.Helper()

	if demo := livetest.DemoName(); demo != "" {
		return livetest.NewDemoRepo(t, target, livetest.DemoRepo(demo), objectFormat, false)
	}

	return livetest.NewBareScratchRepo(t, target, "release-consumer", objectFormat, private)
}

func seedGitLabReleaseCanary(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, files map[string]string) string {
	t.Helper()

	actions := make([]*gitlabapi.CommitActionOptions, 0, len(files))
	for path, content := range files {
		actions = append(actions, &gitlabapi.CommitActionOptions{
			Action: gitlabapi.Ptr(gitlabapi.FileCreate), FilePath: new(path), Content: new(content),
		})
	}

	commit, _, err := client.Commits.CreateCommit(project, &gitlabapi.CreateCommitOptions{
		Branch: new("main"), CommitMessage: new("test: seed native release canary"), Actions: actions,
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	return commit.ID
}

// gitlabProjectVariable is a protected project variable with the environment
// scope that confines it to the jobs of that environment.
type gitlabProjectVariable struct{ name, value, scope string }

// configureGitLabReleaseVariables returns the fingerprint of the GPG key it
// configures, which signs distro packages and the release files of a gpg
// declaration; with distro false it configures none and returns "".
func configureGitLabReleaseVariables(t *testing.T, target livetest.Target, client *gitlabapi.Client, project string, keys releaseKeys, distro bool) string {
	t.Helper()

	storageRequest, storageLimit := "2Gi", "8Gi"
	if distro {
		storageRequest, storageLimit = "4Gi", "20Gi"
	}

	variables := []gitlabProjectVariable{
		{"KUBERNETES_EPHEMERAL_STORAGE_REQUEST", storageRequest, "*"},
		{"KUBERNETES_EPHEMERAL_STORAGE_LIMIT", storageLimit, "*"},
		{"COSIGN_PUBLIC_KEY", keys.cosignPublic, "*"},
		{"RELEASE_SSH_SIGNING_KEY", keys.sshPrivate, "release/prepare"},
		{"RELEASE_COSIGN_PRIVATE_KEY", keys.cosignPrivate, "release/registry/signing"},
		{"RELEASE_COSIGN_PASSPHRASE", keys.passphrase, "release/registry/signing"},
		{"RELEASE_TOKEN", target.Token, "release/publish"},
		{"RELEASE_PACKAGE_TOKEN", target.Token, "release/packages"},
		{"RELEASE_REGISTRY_TOKEN", target.Token, "release/registry/*"},
		{"RELEASE_REGISTRY_USER", target.CredentialUsername, "release/registry/*"},
	}
	fingerprint := ""

	if distro {
		var private string

		private, fingerprint = livetest.GPGKey(t, keys.dir, keys.passphrase)
		variables = append(variables,
			gitlabProjectVariable{"RELEASE_GPG_PRIVATE_KEY", private, "release/registry/signing"},
			gitlabProjectVariable{"RELEASE_GPG_FINGERPRINT", fingerprint, "release/registry/signing"},
			gitlabProjectVariable{"RELEASE_GPG_PASSPHRASE", keys.passphrase, "release/registry/signing"})
	}

	createGitLabProtectedVariables(t, client, project, variables)

	return fingerprint
}

// createGitLabProtectedVariables creates the variables, protected, each in
// its environment scope: a protected variable reaches only the jobs of
// protected refs, and a scoped one only the jobs of its environment.
func createGitLabProtectedVariables(t *testing.T, client *gitlabapi.Client, project string, variables []gitlabProjectVariable) {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	for _, variable := range variables {
		if _, _, variableErr := client.ProjectVariables.CreateVariable(project, &gitlabapi.CreateProjectVariableOptions{
			Key: new(variable.name), Value: new(variable.value), Protected: new(true), EnvironmentScope: new(variable.scope),
		}, gitlabapi.WithContext(ctx)); variableErr != nil {
			t.Fatalf("configure protected variable %s: %v", variable.name, variableErr)
		}
	}
}

// Re-plan the same committed declaration that the trusted Release: plan job
// admitted. No worktree/default-branch file or observed job name selects the
// expected family set. An absent default declaration means no family, just
// as in the existing release planner; any other read/planning failure refuses.
func gitLabReleaseRunnerPlan(t *testing.T, ctx context.Context, client *gitlabapi.Client, project, request, declaration string) appconfig.ReleaseStagePlans {
	t.Helper()

	file := declaration
	if file == "" {
		file = ".forge-ci/artifacts.yml"
	}

	files := fstest.MapFS{}

	body, response, err := client.RepositoryFiles.GetRawFile(project, file, &gitlabapi.GetRawFileOptions{Ref: new(request)}, gitlabapi.WithContext(ctx))
	if err != nil {
		if declaration != "" || response == nil || response.StatusCode != http.StatusNotFound {
			t.Fatalf("read routing declaration at signed request %s: %v", request, err)
		}
	} else {
		files[file] = &fstest.MapFile{Data: body}
	}

	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: file, FS: files}, output.Annotator{}, declaration == "")
	if err != nil {
		t.Fatal(err)
	}

	return plans
}

// The protected template's consumer commands retain the build role, while
// generated version jobs retain the ordinary role; the package job is the
// signer runner's, like the jobs that sign. The existing planner owns
// both the selected families and their exact job names: no prefix exemption
// or second family table may turn an unexpected build into an ordinary job.
type gitLabReleaseRunnerRole struct {
	tags []string
	role runners.Role
}

func gitLabReleaseRunnerRoles(buildTags, runnerTags []string, images, exports bool, baseStages string, plans appconfig.ReleaseStagePlans) (map[string]gitLabReleaseRunnerRole, error) {
	families, err := gitlabpipeline.PlanReleaseFamilies(plans.Prepare, plans.Build)
	if err != nil {
		return nil, err
	}

	build := gitLabReleaseRunnerRole{tags: buildTags, role: runners.Build}
	ordinary := gitLabReleaseRunnerRole{tags: runnerTags, role: runners.Ordinary}
	expected := map[string]gitLabReleaseRunnerRole{"Build: release files": build, "Check: consumer checks": build}

	for _, name := range (gitlabpipeline.ReleaseFamilies{Version: families.Version}).JobNames() {
		expected[name] = ordinary
	}

	for _, name := range (gitlabpipeline.ReleaseFamilies{Build: families.Build}).JobNames() {
		expected[name] = build
	}

	if images {
		expected["Images: build amd64"] = build
	}

	if exports {
		expected["Build: export amd64"] = build
	}

	// A base stage's builds run on the build pool; its other jobs, which run
	// no Containerfile, do not.
	stages, err := gitlabpipeline.ParseBaseStages(baseStages, "")
	if err != nil {
		return nil, err
	}

	for _, stage := range stages {
		expected["Base: build "+stage.Name()] = build
	}

	return expected, nil
}

func requireGitLabReleaseRunnerRouting(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, child int64, expected map[string]gitLabReleaseRunnerRole) {
	t.Helper()

	jobs, response, err := client.Jobs.ListPipelineJobs(project, child, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if response == nil || response.NextPage != 0 {
		t.Fatal("release runner routing requires a complete job listing")
	}

	for _, job := range jobs {
		if job.Runner.ID != 0 {
			t.Logf("release routing: job %q tags=%v runner=%d (%s)", job.Name, job.TagList, job.Runner.ID, job.Runner.Description)
		}
	}

	if err := validateGitLabReleaseRunnerRouting(jobs, expected); err != nil {
		t.Fatal(err)
	}
}

func validateGitLabReleaseRunnerRouting(jobs []*gitlabapi.Job, expected map[string]gitLabReleaseRunnerRole) error {
	seen := map[string]bool{}
	capableRunners := map[int64]bool{}

	for _, job := range jobs {
		if want, routed := expected[job.Name]; routed {
			if seen[job.Name] {
				return fmt.Errorf("release routing lists duplicate planned job %q", job.Name)
			}

			if !slices.Equal(job.TagList, want.tags) || job.Runner.ID == 0 || job.Status != "success" {
				return fmt.Errorf("job %q did not succeed with the requested tags %v", job.Name, want.tags)
			}

			seen[job.Name] = true
			if want.role == runners.Build {
				capableRunners[job.Runner.ID] = true
			}
		} else if len(job.TagList) != 0 {
			return fmt.Errorf("unrelated job %q inherited runner tags %v", job.Name, job.TagList)
		}
	}

	if len(seen) != len(expected) {
		return errors.New("release is missing successful jobs for the requested runner roles")
	}

	for _, job := range jobs {
		if expected[job.Name].role != runners.Build && capableRunners[job.Runner.ID] {
			return fmt.Errorf("non-build job %q ran on a build capability runner %d", job.Name, job.Runner.ID)
		}
	}

	return nil
}

func logGitLabReleaseInventory(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, child int64) {
	t.Helper()

	trace := gitlabTrialTrace(t, ctx, client, project, child, "Build: release files")

	const marker = "Release handoff files (bytes, path relative to dist):"

	_, inventory, found := strings.Cut(trace, marker)
	if found {
		inventory, _, _ = strings.Cut(inventory, "$ rc_build_handoff")
		t.Log(marker + inventory)
	}
}

// requireGitLabPackageSignatures verifies the GPG signature of every distro
// package the release published; a release of no package, such as a jar's,
// has none to verify.
func requireGitLabPackageSignatures(t *testing.T, target livetest.Target, repo string, keys releaseKeys, tag string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), 5*time.Minute)
	defer cancel()

	release := mustReadRelease(ctx, t, target, repo, tag)

	requirePackageSignatures(t, target, keys, release.AssetNames(), func(name string, limit int64) []byte {
		return livetest.Download(t, target, livetest.ReleaseAssetURL(t, target, repo, tag, name), limit)
	})
}

func requireGitLabPublishedImages(t *testing.T, client *gitlabapi.Client, target livetest.Target, repo, publication string, pipeline int64, keys releaseKeys) {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	project := livetest.RepoSlug(target, repo)

	jobs, _, err := client.Jobs.ListPipelineJobs(project, pipeline, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	var signerID int64

	for _, job := range jobs {
		if job.ArtifactsFile.Size > 0 {
			t.Logf("job %s: native artifact archive %d bytes", job.Name, job.ArtifactsFile.Size)
		}

		if job.Name == "Sign: files and images" && job.Status == "success" {
			signerID = job.ID
		}
	}

	if signerID == 0 {
		t.Fatal("the consumer release has no successful signer artifact")
	}

	address := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/artifacts/_release-sign/dist/release-images-publication.json", target.BaseURL(), url.PathEscape(project), signerID)
	// A release that built no image leaves no publication ledger; one that
	// did must publish every image it records.
	ledger, found := livetest.DownloadIfPresent(t, target, address, 1<<20)
	if !found {
		t.Log("the consumer release published no container images")

		return
	}

	entries, err := imageledger.Parse(ledger)
	if err != nil || len(entries) == 0 {
		t.Fatalf("consumer publication ledger: %v", err)
	}

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	expected := registry + "/" + livetest.RepoSlug(target, publication)
	auth := livetest.RegistryAuthConfigDir(t, target, t.TempDir())

	for _, entry := range entries {
		tag, err := name.NewTag(entry.FinalTag)
		if err != nil || tag.Context().Name() != expected {
			t.Fatalf("release image is outside the publication project: %q", entry.FinalTag)
		}

		actual, found := livetest.ImageDigest(t, target, publication, tag.TagStr())
		if !found || actual != entry.Digest {
			t.Fatalf("final image %s does not serve its signed digest", entry.FinalTag)
		}

		ref := expected + "@" + entry.Digest
		verifyCosignSignature(t, target, ref, keys.cosignPublicPath, auth)
		verifyCosignAttestations(t, target, ref, keys.cosignPublicPath, auth, entry.Digest)
	}

	t.Logf("verified %d final images and their attestations in the publication project", len(entries))
}

// requireGitLabReleaseBundles verifies every published signature against the
// release's retained evidence: each Cosign bundle with the trial's Cosign
// key and each OpenPGP .asc as its GPG key's. A release whose declaration
// signs its files with GPG (gpgFiles) signs its checksums so and gives no
// file it signs with GPG a Cosign bundle; any other's checksums bundle is
// also tampered with and must be refused.
func requireGitLabReleaseBundles(t *testing.T, client *gitlabapi.Client, target livetest.Target, repo string, child int64, requestSHA string, keys releaseKeys, tag string, images, gpgFiles bool) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	release := mustReadRelease(ctx, t, target, repo, tag)
	project := livetest.RepoSlug(target, repo)

	jobs, response, err := client.Jobs.ListPipelineJobs(project, child, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if response == nil || response.NextPage != 0 {
		t.Fatal("release artifact binding requires a complete job listing")
	}

	selected, err := livetest.SelectGitLabReleaseEvidenceJobs(jobs, child, requestSHA, images)
	if err != nil {
		t.Fatal(err)
	}

	archive := func(id int64) []byte {
		address := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/artifacts", target.BaseURL(), url.PathEscape(project), id)

		return livetest.GitLabReleaseArchive(t, target, address, fmt.Sprintf("gitlab-release-%d-job-%d.zip", child, id))
	}

	retained, err := livetest.NewReleaseEvidence(target, repo, tag, requestSHA, release.AssetNames(), releaseEvidenceSecrets(keys))
	if err != nil {
		t.Fatal("retain release inventory: ", err)
	}

	var publisherZIP []byte
	if images {
		publisherZIP = archive(selected.Publisher)
	}

	producerZIP, signerZIP := archive(selected.Build), archive(selected.Signer)

	readback, err := retained.CaptureReadback(func(assetName string) ([]byte, error) {
		limit := int64(maxReleaseFile)
		if strings.HasSuffix(assetName, ".apk") {
			limit = 256 << 20
		}

		body := livetest.Download(t, target, livetest.ReleaseAssetURL(t, target, repo, tag, assetName), limit)

		return body, nil
	})
	if err != nil {
		t.Fatal("retain public release readback: ", err)
	}

	evidence, err := livetest.VerifyGitLabReleaseEvidence(livetest.Context(t), livetest.Binary(t), producerZIP, publisherZIP, signerZIP, release.AssetNames(), readback, func(blob, bundle []byte) error {
		return livetest.VerifyBlob(t, target, keys.cosignPublicPath, blob, bundle)
	}, func(blob, signature []byte) error {
		return livetest.VerifyOpenPGPSignature(livetest.Context(t), filepath.Join(keys.dir, "gnupg"), livetest.ToolEnvironment(t, target, nil), keys.gpgFingerprint, blob, signature)
	})
	if err != nil {
		t.Fatal(err)
	}

	if gpgFiles {
		for _, finding := range livetest.GPGReleaseFileFindings(release.AssetNames()) {
			t.Error(finding)
		}
	} else {
		requireReleaseBundleRejections(t, target, keys.cosignPublicPath, evidence.Checksum, evidence.Bundle)
	}

	return evidence.Verified
}

func requireGitLabReleaseChild(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, run *gitlabapi.Pipeline) int64 {
	t.Helper()

	child := requireGitLabChildPipeline(t, ctx, client, project, run)
	if run.Status != "success" {
		// Missing handoffs can originate in a successful producer whose artifact
		// upload only warned. Keep those traces with the downstream failure.
		logGitLabTrace(t, gitlabTrialTrace(t, ctx, client, project, child, "Prepare: changelog", "Build: release files", "Sign: files and images"))
		livetest.FailOnRunConclusionf(t, "native release pipeline ended %s", run.Status)
	}

	return child
}

// requireGitLabChildPipeline is the one child pipeline the parent's trigger
// job created: the release's, or a snapshot's. The pipeline's wait logged
// GitLab's account of a parent that failed, so a missing child names where
// to read why.
func requireGitLabChildPipeline(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, run *gitlabapi.Pipeline) int64 {
	t.Helper()

	bridges, _, err := client.Jobs.ListPipelineBridges(project, run.ID, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatalf("read the parent's trigger job: %v", err)
	}

	if len(bridges) != 1 || bridges[0].DownstreamPipeline == nil {
		livetest.LogGitLabPipelineFailure(t, ctx, client, project, run)
		t.Fatalf("the child pipeline was not created: the parent pipeline %d ended %s with %d trigger jobs; GitLab's account is logged above",
			run.ID, run.Status, len(bridges))
	}

	return bridges[0].DownstreamPipeline.ID
}

const gitlabMinimalBuild = `GOBIN="${RC_WORK}/tools" GOTOOLCHAIN=local go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.12.0
export PATH="${RC_WORK}/tools:${PATH}"
forge-ci build go run --working-dir . --main-package ./cmd/layer-release-canary \
  --binary-name layer-release-canary --artifact-name layer-release-canary \
  --version "$RELEASE_VERSION" --commit "$RELEASE_SHA" --platforms linux/amd64 --build-sbom=true --skip-tests=false
built=dist/linux-amd64/layer-release-canary-linux-amd64
test "$("${built}")" = "layer-release-canary ${RELEASE_VERSION}"
package="layer-release-canary_${RELEASE_VERSION}_linux_amd64"
mkdir -p "${RC_WORK}/package/${package}"
mv -- "${built}" "${RC_WORK}/package/${package}/layer-release-canary"
rmdir dist/linux-amd64
tar --format=ustar --sort=name --mtime='@946684800' --owner=0 --group=0 \
  --numeric-owner -C "${RC_WORK}/package" -cf - "${package}" | gzip -n > "dist/${package}.tar.gz"
cp -- .forge-ci/go-build-sbom/layer-release-canary/bom.json dist/go-build-sbom.json
forge-ci release notes --changelog-file CHANGELOG.md --release-version "$RELEASE_TAG" --target-file dist/release-notes.md
forge-ci release files declare --dist-dir dist --asset "dist/${package}.tar.gz" --sbom dist/go-build-sbom.json \
  --checksums-file dist/checksums.txt --evidence-file dist/release-notes.md
`

// requireGitLabBases holds a release with base stages to its graph: every
// stage's promotion succeeded, and each flavor's final tag in the
// publication project's bases repository serves a digest that carries a
// signature and attestations of the release's Cosign key. It returns the
// stages' names, each promotion's trace by stage, and the projections the
// release handed on, "<digest> <input ID>" each.
func requireGitLabBases(t *testing.T, ctx context.Context, client *gitlabapi.Client, target livetest.Target, project, publication string, child int64, keys releaseKeys, baseStages string) ([]string, map[string]string, []string) {
	t.Helper()

	stages, err := gitlabpipeline.ParseBaseStages(baseStages, "")
	if err != nil {
		t.Fatal(err)
	}

	want, names := 0, make([]string, 0, len(stages))
	for _, stage := range stages {
		want += len(stage.Flavors)
		names = append(names, stage.Name())
	}

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	repository := registry + "/" + livetest.RepoSlug(target, publication) + "/bases"
	auth := livetest.RegistryAuthConfigDir(t, target, t.TempDir())
	projection := regexp.MustCompile(`"ref":"` + regexp.QuoteMeta(repository) + `@(sha256:[0-9a-f]{64})","input_id":"([0-9a-f]{64})"`)

	jobs, _, err := client.Jobs.ListPipelineJobs(project, child, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	traces, projections := map[string]string{}, []string{}

	for _, job := range jobs {
		stage, found := strings.CutPrefix(job.Name, "Base: sign and promote ")
		if !found {
			continue
		}

		if job.Status != "success" {
			t.Fatalf("%s is %s", job.Name, job.Status)
		}

		traces[stage] = gitlabTrialTrace(t, ctx, client, project, child, job.Name)

		// The promotion prints each flavor's verified projection.
		for _, match := range projection.FindAllStringSubmatch(traces[stage], -1) {
			ref := repository + "@" + match[1]
			verifyCosignSignature(t, target, ref, keys.cosignPublicPath, auth)              //nolint:contextcheck // credential proxy lifetime belongs to the test, not this trace context.
			verifyCosignAttestations(t, target, ref, keys.cosignPublicPath, auth, match[1]) //nolint:contextcheck // credential proxy lifetime belongs to the test, not this trace context.

			projections = append(projections, match[1]+" "+match[2])
		}
	}

	if len(projections) != want {
		t.Fatalf("the release verified %d promoted base images, want the %d of %q", len(projections), want, baseStages)
	}

	t.Logf("verified %d promoted base images in %s", len(projections), repository)

	return names, traces, projections
}

// stageGitLabFixtures stages the fixtures settings names, if any, over the
// consumer at root, in its private index alone, as the Forgejo trial does,
// and reports whether the declaration the release reads signs the release
// files with GPG. The fixtures may change how the release signs, never what
// it builds: the profile its images and archives were chosen by must hold.
func stageGitLabFixtures(t *testing.T, opened livetest.Consumer, root string, settings livetest.GitLabReleaseSettings, profile gitlabConsumerProfile) (livetest.Consumer, bool) {
	t.Helper()

	if settings.Fixtures != "" {
		opened = livetest.WithForgejoFixtures(t, opened, settings.Fixtures)
		root = t.TempDir()
		livetest.ForgejoTrialRoot(t, opened, root)

		if _, restaged := gitlabConsumerProfileOf(t, root, settings); !reflect.DeepEqual(restaged, profile) {
			t.Fatalf("the fixtures %s change what the GitLab release builds; they may change its signing alone", settings.Fixtures)
		}
	}

	gpgFiles, err := livetest.SignsReleaseFilesWithGPG(root, profile.artifactsConfig())
	if err != nil {
		t.Fatal(err)
	}

	return opened, gpgFiles
}
