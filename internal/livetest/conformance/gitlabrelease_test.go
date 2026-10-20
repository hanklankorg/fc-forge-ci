// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	gitlabapi "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/retry"
)

func TestGitLabRelease_MinimalSignedArtifacts(t *testing.T) {
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabRelease(t, "", visibility.Private)
		})
	}
}

func TestGitLabRelease_ConsumerTrial(t *testing.T) {
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		livetest.NotCoveredf(t, "use the GitLab consumer-release recipe with a clean reviewed checkout")
	}

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabRelease(t, root, visibility.Private)
		})
	}
}

// Exercise first-push protection without publishing tool images or running a
// release. GitLab can initialize its default rule after the Git push returns.
func TestGitLabRelease_ConsumerBranchProtection(t *testing.T) {
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		livetest.NotCoveredf(t, "consumer branch protection requires a clean reviewed checkout")
	}

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabBranchProtection(t, root, visibility.Private)
		})
	}
}

func TestGitLabRelease_MinimalBranchProtection(t *testing.T) {
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
		Title: gitlabapi.Ptr("fixture release signer"), Key: gitlabapi.Ptr(public), CanPush: gitlabapi.Ptr(true),
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
	archive := os.Getenv("FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE")
	if archive == "" || !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "builder probe requires a reviewed build image archive and GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	repo := livetest.NewScratchRepo(t, target, "build-image-probe")
	image := livetest.PushJobImage(t, target, repo, archive)

	const tag = "v0.0.9-build-probe"
	livetest.PrepareTag(t, target, repo, tag)
	livetest.PublishBinaryAsset(t, target, repo, tag, t.TempDir())
	prelude := livetest.ProbePrelude(target, livetest.ReleaseAssetURL(t, target, repo, tag, "forge-ci"))

	body, err := yaml.Marshal(map[string]any{"probe": map[string]any{
		// As release-jobs.yml declares a build image: its own entrypoint is not the job's shell.
		"image":     map[string]any{"name": image, "entrypoint": []string{""}},
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

func runGitLabRelease(t *testing.T, consumerRoot string, private bool) {
	t.Helper()
	runGitLabReleaseScenario(t, consumerRoot, false, private)
}

// gitlabConsumerSettings returns the caller's settings and the consumer's build
// command, with its variables scoped to the build.
func gitlabConsumerSettings(t *testing.T, consumerRoot string) (livetest.GitLabReleaseSettings, string) {
	t.Helper()

	settings, err := livetest.ParseGitLabReleaseSettings(os.Getenv(consumerSettingsEnv))
	if err != nil {
		t.Fatal(err)
	}

	if consumerRoot == "" && (len(settings.Variables) != 0 || settings.VersionBump != "" || settings.Version != "" || settings.NextVersion != "") {
		t.Fatal("consumer settings require a consumer release trial")
	}

	// A consumer's release builds for the lab's runners, and only for them.
	if consumerRoot != "" {
		var arch string
		if arch, err = livetest.LabRunnerArch(); err != nil {
			t.Fatal(err)
		}

		if settings.Variables, err = livetest.NativeReleasePlatforms(settings.Variables, arch); err != nil {
			t.Fatal(err)
		}
	}

	buildCommand, err := livetest.GitLabConsumerBuildCommand(settings.Variables)
	if err != nil {
		t.Fatal(err)
	}

	return settings, buildCommand
}

// withGitChglog keeps the fixtures and the demo rendering with git-chglog from
// .chglog/, as the layer did before git-cliff became the default.
func withGitChglog(inputs map[string]any) map[string]any {
	inputs["changelog-creator"] = "git-chglog"
	inputs["changelog-config"] = ".chglog/config-keepachangelog.yml"
	inputs["commit-body-config"] = ".chglog/config-minimal.yml"

	return inputs
}

// protectGitLabReleaseTags lets maintainers push release requests and lets
// only maintainers and the signer's deploy key create version tags.
func protectGitLabReleaseTags(ctx context.Context, t *testing.T, client *gitlabapi.Client, project string, deployKeyID int64) {
	t.Helper()

	if _, _, err := client.ProtectedTags.ProtectRepositoryTags(project, &gitlabapi.ProtectRepositoryTagsOptions{
		Name: gitlabapi.Ptr("release-request/*"), CreateAccessLevel: gitlabapi.Ptr(gitlabapi.MaintainerPermissions),
	}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := client.ProtectedTags.ProtectRepositoryTags(project, &gitlabapi.ProtectRepositoryTagsOptions{
		Name: gitlabapi.Ptr("v*"), AllowedToCreate: gitlabapi.Ptr([]*gitlabapi.TagsPermissionOptions{
			{AccessLevel: gitlabapi.Ptr(gitlabapi.MaintainerPermissions)}, {DeployKeyID: gitlabapi.Ptr(deployKeyID)},
		}),
	}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
}

// useGitLabReleasePolicy points the consumer's pipeline at the pinned policy
// file and takes pipeline variables and job-token pushes away from everyone.
func useGitLabReleasePolicy(ctx context.Context, t *testing.T, client *gitlabapi.Client, project, configPath string) {
	t.Helper()

	if _, _, err := client.Projects.EditProject(project, &gitlabapi.EditProjectOptions{
		CIConfigPath:                           gitlabapi.Ptr(configPath),
		CIPipelineVariablesMinimumOverrideRole: gitlabapi.Ptr(gitlabapi.CIPipelineVariablesMinimumOverrideRoleValue("no_one_allowed")),
		CIPushRepositoryForJobTokenAllowed:     gitlabapi.Ptr(false),
	}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
}

func runGitLabReleaseScenario(t *testing.T, consumerRoot string, bases, private bool) {
	t.Helper()

	settings, buildCommand := gitlabConsumerSettings(t, consumerRoot)

	requestTag, finalTag := releaseRequestTag, releaseFinalTag
	if settings.Version != "" {
		requestTag, finalTag = "release-request/v"+settings.Version, "v"+settings.Version
	}

	archives := gitlabReleaseArchiveInputs(t, consumerRoot != "" || bases)
	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "signed GitLab release requires a deployed runner")
	}

	requireGitLabDemoScenario(t, consumerRoot, bases)

	target := livetest.Accept(t, provider.ForgeGitLab)

	var consumer *livetest.Consumer

	if consumerRoot != "" {
		opened := livetest.OpenConsumer(t, target, consumerRoot)
		if livetest.DemoName() != "" {
			opened = opened.WithHistory()
		}

		consumer = &opened
	}

	layer := livetest.PushLayer(t, target, gitlabReleaseRepo(t, target, "release-engine"))
	images := pushGitLabReleaseImages(t, target, archives)
	jobImage, buildImage, signerImage := images.job, images.build, images.signer
	publication := gitlabReleaseRepo(t, target, "release-publication")
	policyRepo := gitlabReleaseRepo(t, target, "release-owned-policy")

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
	requestKeyDir := t.TempDir()
	_, requestPublic := livetest.SSHKey(t, requestKeyDir)

	gitURL, hostKey, supported := livetest.SSHRoute(t, target, repo)
	if !supported {
		t.Fatal("the selected GitLab endpoint needs a pinned runner-side SSH route")
	}

	deploy, _, err := client.DeployKeys.AddDeployKey(project, &gitlabapi.AddDeployKeyOptions{
		Title: gitlabapi.Ptr("fixture release signer"), Key: gitlabapi.Ptr(keys.sshPublic), CanPush: gitlabapi.Ptr(true),
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	protectGitLabReleaseTags(ctx, t, client, project, deploy.ID)

	// A kept demo holds no key or token once the run is over.
	if livetest.DemoName() != "" {
		livetest.DeleteSecretsAfter(t, target, repo, gitlabReleaseSecrets)
	}

	configureGitLabReleaseVariables(t, target, client, project, keys, consumer != nil)

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	checks, build := []string{"go test ./..."}, []string{gitlabMinimalBuild}
	if consumer != nil {
		checks = []string{"bash .gitlab/check-release.sh"}
		build = []string{buildCommand}
	}

	if bases {
		checks, build = []string{"test -f CHANGELOG.md"}, []string{gitlabBaseBuild}
	}

	policyIncludes := []any{map[string]any{
		"component": strings.TrimPrefix(target.BaseURL(), "https://") + "/" + layer.Repository + "/release@" + layer.Commit,
		"inputs": withGitChglog(map[string]any{
			"forge-ci-repository": layer.Repository, "consumer-project": project,
			"job-image": jobImage, "build-image": buildImage, "signer-image": signerImage,
			"publication-project":          livetest.RepoSlug(target, publication),
			"publication-image-repository": registry + "/" + livetest.RepoSlug(target, publication),
			"cosign-public-key-sha256":     keys.cosignPublicSHA256,
			"release-git-url":              gitURL, "release-git-host-key-fingerprint": hostKey,
			"request-allowed-signers": "livetest@localhost " + requestPublic,
			"bot-name":                "Layer acceptance", "bot-email": releaseBotEmail,
			"sign-distro-packages": consumer != nil,
			"version-bump":         settings.VersionBump,
			"base-images":          bases,
			"checks":               checks,
			"build":                build,
		}),
	}}
	// The consumer's own merge-request gate, for merge-request pipelines only,
	// read at the merge request's commit: docs/gitlab.md, The merge-request
	// gate under a policy. Release-request tag pipelines stay policy-only.
	gate := consumer != nil && consumer.HasGitLabGate(t)
	if gate {
		policyIncludes = append([]any{gitlabGateInclude(project)}, policyIncludes...)
	}

	policyConfig := map[string]any{}

	if bases {
		policyIncludes = append(policyIncludes, map[string]any{
			"component": strings.TrimPrefix(target.BaseURL(), "https://") + "/" + layer.Repository + "/check@" + layer.Commit,
			"inputs": map[string]any{"forge-ci-repository": layer.Repository, "job-image": jobImage,
				"job-name": "candidate-check", "commands": []string{"test -f warm-request.txt"}},
		})
		policyConfig["candidate-check"] = map[string]any{
			"rules": []any{map[string]string{"if": `$CI_PIPELINE_SOURCE == "merge_request_event"`}},
		}
	}

	policyConfig["include"] = policyIncludes

	policyBody, err := yaml.Marshal(policyConfig)
	if err != nil {
		t.Fatal(err)
	}

	policy := gitlabPolicyCommit(t, ctx, client, livetest.RepoSlug(target, policyRepo), "release.yml", string(policyBody), gitlabapi.FileCreate)
	useGitLabReleasePolicy(ctx, t, client, project, "release.yml@"+livetest.RepoSlug(target, policyRepo)+":"+policy.ID)

	livetest.RestrictGitLabJobToken(t, target, repo)

	files := map[string]string{
		"go.mod": "module example.invalid/layer-release-canary\n\ngo 1.27.2\n",
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
		request = consumer.PushGitLabRelease(t, repo, layer, livetest.GitLabGateImages{Job: jobImage, Lint: images.lint})
	} else {
		request = seedGitLabReleaseCanary(t, ctx, client, project, files)
	}

	requireObjectFormats(t, objectFormat, request, layer.Commit)
	livetest.SignGitLabRequest(t, target, repo, request, requestTag, filepath.Join(requestKeyDir, "lab-ssh"))
	run := waitGitLabTagPipeline(t, ctx, client, project, requestTag, request)

	child := requireGitLabReleaseChild(t, ctx, client, project, run)
	if consumer != nil {
		logGitLabReleaseInventory(t, ctx, client, project, child)
	}

	requireReleaseGit(t, target, repo, keys, request, requestTag, finalTag)
	verified := requireGitLabReleaseBundles(t, target, repo, keys, finalTag)
	t.Logf("verified %d published Cosign bundles", verified)

	if consumer != nil {
		requireGitLabPackageSignatures(t, target, repo, keys, finalTag)
		requireGitLabPublishedImages(t, client, target, repo, publication, child, keys)
	}

	t.Logf("GitLab release %s: parent %d, child %d, %d verified bundles", finalTag, run.ID, child, verified)
	if settings.VersionBump == "npm" {
		requireGitLabNPMVersionContract(t, ctx, client, target, repo, finalTag)
	}
	if settings.NextVersion != "" {
		parent := mergeGitLabNextCandidate(t, client, target, repo)
		requestTag, finalTag := "release-request/v"+settings.NextVersion, "v"+settings.NextVersion
		livetest.SignGitLabRequest(t, target, repo, parent, requestTag, filepath.Join(requestKeyDir, "lab-ssh"))
		next := waitGitLabTagPipeline(t, ctx, client, project, requestTag, parent)
		child := requireGitLabReleaseChild(t, ctx, client, project, next)
		requireReleaseGit(t, target, repo, keys, parent, requestTag, finalTag)
		bundles := requireGitLabReleaseBundles(t, target, repo, keys, finalTag)
		if consumer != nil {
			requireGitLabPackageSignatures(t, target, repo, keys, finalTag)
			requireGitLabPublishedImages(t, client, target, repo, publication, child, keys)
		}
		t.Logf("second consecutive GitLab release %s: parent commit %s, pipeline %d, child %d, %d verified bundles", finalTag, parent, next.ID, child, bundles)
		if settings.VersionBump == "npm" {
			requireGitLabNPMVersionContract(t, ctx, client, target, repo, finalTag)
		}
	}

	if bases {
		requireGitLabBaseReuse(t, client, target, repo, publication, keys, requestKeyDir)
	}

	if gate {
		requireGitLabPolicyMergeRequest(t, client, project, repo, *consumer, request)
	}
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

	if waitErr := retry.Run(ctx, nil, 30, 2*time.Second, func() error {
		branch, _, readErr := client.Branches.GetBranch(project, trialBranch, gitlabapi.WithContext(ctx))
		if readErr != nil || branch.Commit == nil || branch.Commit.ID != head {
			return fmt.Errorf("branch %s is not at %s yet: %w", trialBranch, head, errs.ErrDependencyUnavailable)
		}

		return nil
	}, retry.WithBackoff(retry.Constant)); waitErr != nil {
		t.Fatal(waitErr)
	}

	mr, err := livetest.CreateGitLabMergeRequest(ctx, client, project, &gitlabapi.CreateMergeRequestOptions{
		SourceBranch: gitlabapi.Ptr(trialBranch), TargetBranch: gitlabapi.Ptr("main"), Title: gitlabapi.Ptr("test: consumer trial"),
	})
	if err != nil {
		t.Fatal(err)
	}

	pipeline := waitGitLabMRPipeline(t, ctx, client, project, mr.IID, head, 0)
	t.Logf("GitLab MR !%d at %s under the release policy: pipeline %d ended %s", mr.IID, head, pipeline.ID, pipeline.Status)

	if pipeline.Status != "success" {
		logGitLabTrace(t, gitlabTrialTrace(t, ctx, client, project, pipeline.ID))
		t.Fatalf("the consumer's gate under the release policy ended %s", pipeline.Status)
	}
}

// gitlabReleaseArchives are the prepared image archives of a release's jobs;
// build is empty when the release builds with the job image, lint when the
// consumer's merge-request gate has no nanolinter component.
type gitlabReleaseArchives struct{ job, build, signer, lint string }

// gitlabReleaseArchiveInputs reads the archives the recipe names, and reports
// the scenario not covered when one it needs is missing.
func gitlabReleaseArchiveInputs(t *testing.T, needBuild bool) gitlabReleaseArchives {
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

	return archives
}

// gitlabReleaseImages are the image references of a release's jobs.
type gitlabReleaseImages struct{ job, build, signer, lint string }

// pushGitLabReleaseImages pushes each archive to a project of its own and
// returns the image references.
func pushGitLabReleaseImages(t *testing.T, target livetest.Target, archives gitlabReleaseArchives) gitlabReleaseImages {
	t.Helper()

	images := gitlabReleaseImages{job: livetest.PushJobImage(t, target, gitlabReleaseRepo(t, target, "release-job-image"), archives.job)}

	images.build = images.job
	if archives.build != "" {
		images.build = livetest.PushJobImage(t, target, gitlabReleaseRepo(t, target, "release-build-image"), archives.build)
	}

	images.signer = livetest.PushJobImage(t, target, gitlabReleaseRepo(t, target, "release-signer-image"), archives.signer)
	if archives.lint != "" {
		images.lint = livetest.PushJobImage(t, target, gitlabReleaseRepo(t, target, "release-lint-image"), archives.lint)
	}

	return images
}

// requireGitLabDemoScenario refuses demo mode for anything but a consumer's
// release.
func requireGitLabDemoScenario(t *testing.T, consumerRoot string, bases bool) {
	t.Helper()

	if livetest.DemoName() != "" && (consumerRoot == "" || bases) {
		t.Fatal("demo mode shows a consumer's release; use `just demo gitlab <name>`")
	}
}

// gitlabGateInclude admits the consumer's merge-request gate into the policy,
// for merge-request pipelines only, at the merge request's commit.
func gitlabGateInclude(project string) map[string]any {
	return map[string]any{
		"project": project, "ref": "$CI_COMMIT_SHA", "file": livetest.GitLabGateFile,
		"rules": []any{map[string]string{"if": `$CI_PIPELINE_SOURCE == "merge_request_event"`}},
	}
}

// gitlabReleaseSecrets are the release project's variables that hold a key, a
// passphrase or a token.
var gitlabReleaseSecrets = []string{ //nolint:gochecknoglobals // Fixed list shared by setup and the demo's cleanup.
	"RELEASE_SSH_SIGNING_KEY", "RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_COSIGN_PASSPHRASE", "RELEASE_TOKEN",
	"RELEASE_REGISTRY_TOKEN", "RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_FINGERPRINT", "RELEASE_GPG_PASSPHRASE",
}

// gitlabReleaseRepo makes one of a release's helper projects: a scratch one a
// trial resets and deletes, or in demo mode a kept one named after the demo.
func gitlabReleaseRepo(t *testing.T, target livetest.Target, scenario string) string {
	t.Helper()

	if demo := livetest.DemoName(); demo != "" {
		return livetest.NewDemoRepo(t, target, livetest.DemoHelperRepo(demo, strings.TrimPrefix(scenario, "release-")), "sha1", true)
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
			Action: gitlabapi.Ptr(gitlabapi.FileCreate), FilePath: gitlabapi.Ptr(path), Content: gitlabapi.Ptr(content),
		})
	}

	commit, _, err := client.Commits.CreateCommit(project, &gitlabapi.CreateCommitOptions{
		Branch: gitlabapi.Ptr("main"), CommitMessage: gitlabapi.Ptr("test: seed native release canary"), Actions: actions,
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	return commit.ID
}

func configureGitLabReleaseVariables(t *testing.T, target livetest.Target, client *gitlabapi.Client, project string, keys releaseKeys, distro bool) {
	t.Helper()

	storageRequest, storageLimit := "2Gi", "8Gi"
	if distro {
		storageRequest, storageLimit = "4Gi", "20Gi"
	}

	type projectVariable struct{ name, value, scope string }

	variables := []projectVariable{
		{"KUBERNETES_EPHEMERAL_STORAGE_REQUEST", storageRequest, "*"},
		{"KUBERNETES_EPHEMERAL_STORAGE_LIMIT", storageLimit, "*"},
		{"COSIGN_PUBLIC_KEY", keys.cosignPublic, "*"},
		{"RELEASE_SSH_SIGNING_KEY", keys.sshPrivate, "release/prepare"},
		{"RELEASE_COSIGN_PRIVATE_KEY", keys.cosignPrivate, "release/registry/signing"},
		{"RELEASE_COSIGN_PASSPHRASE", keys.passphrase, "release/registry/signing"},
		{"RELEASE_TOKEN", target.Token, "release/publish"},
		{"RELEASE_REGISTRY_TOKEN", target.Token, "release/registry/*"},
		{"RELEASE_REGISTRY_USER", target.CredentialUsername, "release/registry/*"},
	}
	if distro {
		private, fingerprint := livetest.GPGKey(t, keys.dir, keys.passphrase)
		variables = append(variables,
			projectVariable{"RELEASE_GPG_PRIVATE_KEY", private, "release/registry/signing"},
			projectVariable{"RELEASE_GPG_FINGERPRINT", fingerprint, "release/registry/signing"},
			projectVariable{"RELEASE_GPG_PASSPHRASE", keys.passphrase, "release/registry/signing"})
	}

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	for _, variable := range variables {
		if _, _, variableErr := client.ProjectVariables.CreateVariable(project, &gitlabapi.CreateProjectVariableOptions{
			Key: gitlabapi.Ptr(variable.name), Value: gitlabapi.Ptr(variable.value), Protected: gitlabapi.Ptr(true), EnvironmentScope: gitlabapi.Ptr(variable.scope),
		}, gitlabapi.WithContext(ctx)); variableErr != nil {
			t.Fatalf("configure protected variable %s: %v", variable.name, variableErr)
		}
	}
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
	verified := 0

	for _, asset := range release.AssetNames() {
		if !strings.HasSuffix(asset, ".deb") && !strings.HasSuffix(asset, ".rpm") && !strings.HasSuffix(asset, ".apk") {
			continue
		}

		if _, present := release.Asset(asset + ".sig"); !present {
			t.Fatalf("package %s has no published GPG sidecar", asset)
		}

		dir := t.TempDir()
		for _, item := range []struct{ asset, file string }{{asset, "package"}, {asset + ".sig", "signature"}} {
			body := livetest.Download(t, target, livetest.ReleaseAssetURL(t, target, repo, tag, item.asset), maxReleaseFile)
			if err := os.WriteFile(filepath.Join(dir, item.file), body, 0o600); err != nil {
				t.Fatal(err)
			}
		}

		command := exec.CommandContext(ctx, "gpg", "--homedir", filepath.Join(keys.dir, "gnupg"), "--batch", "--no-auto-key-retrieve", "--verify", filepath.Join(dir, "signature"), filepath.Join(dir, "package")) //nolint:gosec // Generated fixture keyring and fixed owned download paths; no ambient trust or key lookup.

		command.Env = livetest.ToolEnvironment(t, target, nil)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("package signature failed for %s: %v\n%s", asset, err, output)
		}

		verified++
	}

	t.Logf("verified %d published GPG package sidecars", verified)
}

func requireGitLabPublishedImages(t *testing.T, client *gitlabapi.Client, target livetest.Target, repo, publication string, pipeline int64, keys releaseKeys) {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	project := livetest.RepoSlug(target, repo)

	jobs, _, err := client.Jobs.ListPipelineJobs(project, pipeline, &gitlabapi.ListJobsOptions{ListOptions: gitlabapi.ListOptions{PerPage: 100}}, gitlabapi.WithContext(ctx))
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

func requireGitLabReleaseBundles(t *testing.T, target livetest.Target, repo string, keys releaseKeys, tag string) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	release := mustReadRelease(ctx, t, target, repo, tag)
	verified := 0

	for _, assetName := range release.AssetNames() {
		plain, bundle := strings.CutSuffix(assetName, ".bundle")
		if !bundle {
			continue
		}

		_, present := release.Asset(plain)
		if !present {
			t.Fatalf("signature %s has no published subject", assetName)
		}

		if verifyErr := livetest.VerifyBlob(t, target, keys.cosignPublicPath,
			livetest.Download(t, target, livetest.ReleaseAssetURL(t, target, repo, tag, plain), maxReleaseFile),
			livetest.Download(t, target, livetest.ReleaseAssetURL(t, target, repo, tag, assetName), maxReleaseFile)); verifyErr != nil {
			t.Fatalf("published signature failed verification: %v", verifyErr)
		}

		verified++
	}

	// Container-first releases can carry their SBOM in image attestations,
	// rather than as a standalone release asset. Require the mandatory subjects
	// by name instead of treating three arbitrary bundles as completeness.
	for _, subject := range []string{"checksums.txt", releaseProvenance} {
		if _, present := release.Asset(subject + ".bundle"); !present {
			t.Fatalf("release has no bundle for required subject %s", subject)
		}
	}

	return verified
}

func requireGitLabReleaseChild(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, run *gitlabapi.Pipeline) int64 {
	t.Helper()

	bridges, _, err := client.Jobs.ListPipelineBridges(project, run.ID, nil, gitlabapi.WithContext(ctx))
	if err != nil || len(bridges) != 1 || bridges[0].DownstreamPipeline == nil {
		t.Fatalf("release child pipeline was not created: %v", err)
	}

	child := bridges[0].DownstreamPipeline.ID
	if run.Status != "success" {
		detail, _, detailErr := client.Pipelines.GetPipeline(project, child, gitlabapi.WithContext(ctx))
		if detailErr != nil {
			t.Logf("read child pipeline configuration: %v", detailErr)
		} else if detail.YamlErrors != "" {
			t.Logf("child pipeline configuration: %s", detail.YamlErrors)
		}

		// Missing handoffs can originate in a successful producer whose artifact
		// upload only warned. Keep those traces with the downstream failure.
		logGitLabTrace(t, gitlabTrialTrace(t, ctx, client, project, child, "Prepare: changelog", "Build: release files", "Sign: files and images"))
		t.Fatalf("native release pipeline ended %s", run.Status)
	}

	return child
}

const gitlabMinimalBuild = `GOBIN="${RC_WORK}/tools" GOTOOLCHAIN=local go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.10.0
export PATH="${RC_WORK}/tools:${PATH}"
forge-ci build go run --working-dir . --main-package ./cmd/layer-release-canary \
  --binary-name layer-release-canary --artifact-name layer-release-canary \
  --version "$RELEASE_VERSION" --commit "$RELEASE_SHA" --platforms linux/amd64 --build-sbom=true --skip-tests=true
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
