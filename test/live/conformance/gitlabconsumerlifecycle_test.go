// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// The jobs of the policy's snapshot-images component that hold a snapshot's
// frozen plan, in the parent pipeline, and its receipts, in the child.
const (
	gitlabSnapshotPlanJob    = "Snapshot: plan"
	gitlabSnapshotCollectJob = "Snapshot: collect"
)

// gitlabLifecyclePolicyFile is the one policy file of a lifecycle's project,
// whose includes serve its three pipelines.
const gitlabLifecyclePolicyFile = "lifecycle.yml"

// gitlabMainPushRule admits an include for the push pipelines that move main
// alone: the push a merge makes, not the push that creates main, not a tag's
// push, whose ref name is the tag's, and not a merge request's pipeline.
// GitLab gives a push that creates a branch an all-zero CI_COMMIT_BEFORE_SHA.
// It evaluates a push's pipeline in a later background job, which may run
// after the policy is in place, so the push that seeds main must match no
// rule of the policy rather than rely on arriving first. All three variables
// are pre-pipeline variables, which GitLab documents for include rules.
const gitlabMainPushRule = `$CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_REF_NAME == "main" && $CI_COMMIT_BEFORE_SHA !~ /^0+$/`

// A consumer lifecycle on GitLab: the linked flow TestForgejoLayer_ConsumerLifecycle
// runs, in one GitLab project under one pinned policy. It is a demo's with a
// development route on GitLab, a service snapshot of its one container, which
// the snapshot-images component builds from the declaration the demo's
// release builds its images from, whatever its ecosystem and build type:
//
//  1. a merge request from development into main, judged by the consumer's
//     own merge-request gate, which the policy includes for merge-request
//     pipelines;
//  2. its native merge through the API, told the head commit, with a merge
//     commit of the base and the change;
//  3. the service development snapshot: the policy's snapshot-images
//     component runs on the push the merge makes to main and snapshots
//     development in a child pipeline, which builds the producers the
//     declaration names in their toolchain images, builds the image with
//     the selected builder and publishes one amd64 image;
//  4. a signed release request at the merged main, judged as a release
//     trial's: main advanced by one signed release commit, signed tag, a
//     release with Cosign bundles that verify, signed packages and images;
//  5. the project holds those three pipelines, the two skipped pipelines
//     GitLab records for the release commit's "[skip ci]" pushes, two tags,
//     one release and the bot's deploy key, and nothing else.
//
// Each phase ends the lifecycle when it fails, and says which phase it was.
// The release requester's trust is the policy's request-allowed-signers input,
// not a file of the consumer's, so nothing is staged for it. The credentials
// are protected variables, which reach only the pipelines of protected refs,
// main and the request tag, each scoped to the environment of the jobs that
// use it: the merge request's pipeline gets none, the snapshot's publisher
// the development registry credential alone, the release's jobs the
// release's, given at the release phase and not before. The bot's deploy
// key alone exists from the start, because main is protected to it before
// its first write. The project's pipeline configuration is set after the
// base and the change are pushed, as the Forgejo lifecycle turns Actions on
// then: the merge's push is the first on main the policy sees.
func TestGitLabLayer_ConsumerLifecycle(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	const usage = "just test-live-consumer gitlab <checkout> lifecycle job-archive=<path> build-archive=<path> signer-archive=<path> lint-archive=<path>"

	forEachConsumerCheckout(t, provider.ForgeGitLab, usage, func(t *testing.T, root string) {
		t.Helper()

		if livetest.DemoName() != "" {
			t.Fatal("a lifecycle runs in disposable scratch projects, not in a kept demo")
		}

		runGitLabConsumerLifecycle(t, root)
	}, livetest.TrialLifecycle)
}

// gitlabLifecycleRow is what the visibilities of one lifecycle row share:
// what a release row shares, the declaration the snapshot builds from and
// the selected image builder.
type gitlabLifecycleRow struct {
	gitlabReleaseRow

	snapshotSource string
	builder        livetest.ImageBuilderChoice
}

// runGitLabConsumerLifecycle runs the lifecycle once for each visibility,
// pushing the job images once before them, as a release does
// (runGitLabReleaseScenario).
func runGitLabConsumerLifecycle(t *testing.T, root string) {
	t.Helper()

	visibilities := livetest.ConsumerVisibilities(t)

	if event := os.Getenv(consumerEventEnv); event != livetest.TrialLifecycle {
		t.Fatalf("the event is %s; a lifecycle starts with %s", event, livetest.TrialLifecycle)
	}

	parsed, err := livetest.ParseGitLabLifecycleSettings(os.Getenv(consumerSettingsEnv))
	if err != nil {
		t.Fatal(err)
	}

	// The development phase snapshots the consumer's one container, from the
	// declaration its release builds its images from; the release phase
	// releases what the consumer declares, as a release does.
	settings, profile := gitlabConsumerProfileOf(t, root, parsed)

	snapshotSource, err := livetest.ServiceSnapshotSource(root)
	if err != nil {
		t.Fatal(err)
	}

	// The snapshot's image build and the release's image jobs run in the
	// selected builder's job image; the consumer's build, in the snapshot's
	// producer job as in the release's build job, in the build image, the
	// toolchain image of the consumer's ecosystem.
	archives := gitlabReleaseArchiveInputs(t, true, true, profile.kaniko)
	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "a lifecycle needs a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	builder := livetest.ChooseImageBuilder(t, target)

	if platforms := settings.Variables["RELEASE_PLATFORMS"]; platforms != "linux/amd64" {
		livetest.NotCoveredf(t, "the snapshot and the release build images for %s only on tagged arm64 runners, which the lab has none of", platforms)
	}

	pushed := livetest.JobImages{}
	row := gitlabLifecycleRow{
		consumerRoot: root, settings: settings, profile: profile,
		archives: archives, images: pushGitLabReleaseImages(t, target, archives, pushed), pushed: pushed,
		snapshotSource: snapshotSource, builder: builder,
	}

	for _, visibility := range visibilities {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabConsumerLifecycleVisibility(t, row, visibility.Private)
		})
	}
}

func runGitLabConsumerLifecycleVisibility(t *testing.T, row gitlabLifecycleRow, private bool) {
	t.Helper()

	root, settings, profile, snapshotSource := row.consumerRoot, row.settings, row.profile, row.snapshotSource
	buildTags := row.builder.RunnerLabels

	target := livetest.Accept(t, provider.ForgeGitLab)

	opened := livetest.OpenConsumer(t, target, root)
	if !opened.HasGitLabGate(t) {
		t.Fatalf("the consumer has no %s, which a lifecycle's merge request is judged by", livetest.GitLabGateFile)
	}

	// A lifecycle's checks run on the ordinary runner, whichever builder the
	// run selects. The one fixture whose checks run Buildah themselves is the
	// release trial's subject, not a lifecycle's.
	if opened.GitLabChecksNeedBuildah(t) {
		t.Fatal("the consumer's checks run Buildah themselves; a lifecycle's checks run on the ordinary runner, so run that fixture as a release trial")
	}

	layer := livetest.PushLayer(t, target, gitlabReleaseRepo(t, target, "lifecycle-engine", livetest.ReadLayerAnonymously))
	consumer := opened.WithDraftLayer(t, layer).WithServiceSnapshotDeclaration(t, snapshotSource)
	families := livetest.ServiceSnapshotFamilies(livetest.Context(t), t, consumer)
	images := row.images
	images.kaniko = pushGitLabKanikoJobImage(t, target, row.archives, layer)
	publication := gitlabReleaseRepo(t, target, "lifecycle-publication", "")
	policyRepo := gitlabReleaseRepo(t, target, "lifecycle-policy", "")
	policyProject := livetest.RepoSlug(target, policyRepo)
	repo := livetest.NewBareScratchRepo(t, target, livetest.ConsumerLifecycle, consumer.ObjectFormat, private)
	project := livetest.RepoSlug(target, repo)

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(livetest.Context(t), 90*time.Minute)
	defer cancel()

	keys := newReleaseKeys(t)
	livetest.RetainGitLabJobLogsAfter(t, target, repo, releaseEvidenceSecrets(keys))
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
	// The development registry credential, for the snapshot's publisher alone.
	createGitLabProtectedVariables(t, client, project, []gitlabProjectVariable{
		{"SNAPSHOT_REGISTRY_USER", target.CredentialUsername, "snapshot/registry"},
		{"SNAPSHOT_REGISTRY_PASSWORD", target.Token, "snapshot/registry"},
	})

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	image := registry + "/" + strings.ToLower(project)

	imageBuilder, builderJobImage := gitlabImageBuilder(images)

	releasePolicy := gitlabReleasePolicy{
		project: project, publication: livetest.RepoSlug(target, publication), registry: registry,
		images: images, keys: keys, gitURL: gitURL, hostKey: hostKey, requestPublic: requestPublic,
		settings: settings, distro: true,
		checks: []string{"bash .gitlab/check-release.sh"}, runnerTags: []string{},
		build: []string{profile.build}, buildRunnerTags: buildTags,
		releaseImages: profile.releaseImages, artifactsConfig: profile.artifactsConfig(), releasePlatforms: "linux/amd64",
		imageBuilder: imageBuilder, builderJobImage: builderJobImage, export: profile.export, changelog: profile.changelog,
	}

	// The snapshot component takes the development phase: the consumer's
	// declaration, the snapshot's image repository in the project's own
	// registry, each producer's build in the build image, its ecosystem's
	// toolchain image, the image build with the selected builder on its
	// runner. It runs on the push pipelines that move main.
	snapshotInputs := map[string]any{
		"branch": lifecycleBranch, "artifacts-config": livetest.ServiceSnapshotDeclarationFile,
		"image-repository": image, "forge-ci-repository": layer.Repository,
		"server-url": target.BaseURL(), "registry": registry,
		"job-image": images.job, "build-image": builderJobImage,
		"image-builder": imageBuilder, "builder-job-image": builderJobImage,
		"runner-tags": []string{}, "build-runner-tags-linux-amd64": buildTags, "build-runner-tags-linux-arm64": []string{},
	}

	for _, family := range families {
		snapshotInputs["artifact-job-image-"+family] = images.build
	}

	snapshotInclude := map[string]any{
		"component": gitlabComponent(target, layer, "snapshot-images"),
		"inputs":    layer.ComponentAdminInputs(t, "snapshot-images", snapshotInputs),
		"rules":     []any{map[string]string{"if": gitlabMainPushRule}},
	}

	gateImages := livetest.GitLabGateImages{Job: images.job, Lint: images.lint, ExtraJobs: livetest.PushGitLabGateExtraJobImages(t, target, consumer, maps.Clone(row.pushed), func(name string) string {
		return gitlabReleaseRepo(t, target, "lifecycle-job-image-"+strings.ToLower(name), livetest.ReadJobImageAnonymously)
	})}
	gateCommit := gitlabPolicyCommit(t, ctx, client, policyProject, livetest.GitLabAdminGateFile, consumer.GitLabAdminPolicy(t, layer, gateImages), gitlabapi.FileCreate)

	policyBody, err := yaml.Marshal(map[string]any{"include": []any{
		gitlabGateInclude(policyProject, gateCommit.ID), snapshotInclude, releasePolicy.include(t, target, layer),
	}})
	if err != nil {
		t.Fatal(err)
	}

	policy := gitlabPolicyCommit(t, ctx, client, policyProject, gitlabLifecyclePolicyFile, string(policyBody), gitlabapi.FileCreate)

	livetest.RestrictGitLabJobToken(t, target, repo)
	// Protect the empty project before its first write.
	livetest.RestrictGitLabReleaseBranch(t, target, repo, deploy.ID)

	t.Logf("consumer %s at %s (%s); layer %s at %s; job image %s, build image %s for producers %v of %s, image builder %s", root, consumer.Commit, consumer.ObjectFormat, layer.Repository, layer.Commit, images.job, images.build, families, snapshotSource, imageBuilder)

	// Phase 1: the merge request.
	base := consumer.PushGitLabRelease(t, repo, layer, gateImages)
	requireObjectFormats(t, consumer.ObjectFormat, base, layer.Commit)

	head := consumer.PushChange(t, repo, lifecycleBranch, base)

	// Nothing runs for these two pushes, whenever GitLab evaluates them: they
	// create main and the change branch, which no rule of the policy admits.
	useGitLabAdminPolicy(ctx, t, client, project, gitlabLifecyclePolicyFile+"@"+policyProject+":"+policy.ID)
	// The merge request waits for GitLab to process both pushes: one it
	// processed later would refresh the open merge request.
	waitGitLabPush(t, ctx, client, project, layerMain, base)
	waitGitLabPush(t, ctx, client, project, lifecycleBranch, head)

	mr, err := livetest.CreateGitLabMergeRequest(ctx, client, project, &gitlabapi.CreateMergeRequestOptions{
		SourceBranch: new(lifecycleBranch), TargetBranch: new(layerMain), Title: new("test: consumer lifecycle"),
	})
	if err != nil {
		t.Fatal(err)
	}

	review := waitGitLabMRPipeline(t, ctx, client, project, mr.IID, head, 0)
	requireGitLabLifecyclePipeline(t, ctx, client, project, "1 merge request", review)
	t.Log(strings.ReplaceAll(gitlabTrialTrace(t, ctx, client, project, review.ID, "Check: build and test", "Check: lint"), target.Token, "[REDACTED]"))
	requireGitLabPipelineIDs(t, ctx, client, project, review.ID)
	requireGitLabNoReleases(t, ctx, client, project)
	t.Logf("lifecycle phase 1 merge request: !%d, %s@%s into %s@%s, pipeline %d", mr.IID, lifecycleBranch, head, layerMain, base, review.ID)

	// Phase 2: the native merge.
	merged, err := livetest.MergeGitLabMergeRequest(ctx, client, project, mr.IID, head)
	if err != nil {
		t.Fatalf("lifecycle phase 2 merge: %v", err)
	}

	request := merged.MergeCommitSHA
	requireBranches(t, target, repo, map[string]string{layerMain: request, lifecycleBranch: head})
	livetest.MirrorRepo(t, target, repo).RequireMergeOf(t, request, base, head)
	t.Logf("lifecycle phase 2 merge: !%d merged natively, %s at %s with parents %s and %s", mr.IID, layerMain, request, base, head)

	// Phase 3: the service development snapshot, on the push the merge made.
	development := waitGitLabPushPipeline(t, ctx, client, project, layerMain, request)
	snapshot := requireGitLabChildPipeline(t, ctx, client, project, development)
	requireGitLabLifecyclePipeline(t, ctx, client, project, "3 development", development, snapshot)

	receipt, expected := requireGitLabServiceSnapshot(t, ctx, client, target, repo, consumer, development.ID, snapshot, head, image)
	requireGitLabPipelineIDs(t, ctx, client, project, review.ID, development.ID)
	requireGitLabNoReleases(t, ctx, client, project)
	t.Logf("lifecycle phase 3 development: pipeline %d on %s@%s, child %d, snapshots %s@%s as %s; producers %d", development.ID, layerMain, request, snapshot, lifecycleBranch, head, receipt.Ref, len(expected.Artifacts))

	// Phase 4: the signed release request at the merged main. The release's
	// authority is given now and not before.
	keys.gpgFingerprint = configureGitLabReleaseVariables(t, target, client, project, keys, true)
	awaitGitLabPackages(t, target, repo, profile.packages, profile.finalTag)
	livetest.SignGitLabRequest(t, target, repo, request, profile.requestTag, filepath.Join(requestKeyDir, "lab-ssh"))

	run := waitGitLabTagPipeline(t, ctx, client, project, profile.requestTag, request)
	child := requireGitLabReleaseChild(t, ctx, client, project, run)
	logGitLabReleaseInventory(t, ctx, client, project, child)

	released := requireReleaseGit(t, target, repo, keys, request, profile.requestTag, profile.finalTag)

	gpgFiles, err := livetest.SignsReleaseFilesWithGPG(root, profile.artifactsConfig())
	if err != nil {
		t.Fatal(err)
	}

	verified := requireGitLabReleaseBundles(t, client, target, repo, child, run.SHA, keys, profile.finalTag, profile.releaseImages, gpgFiles)

	if run.SHA != request {
		t.Fatal("release routing plan must use the exact signed request commit")
	}

	plans := gitLabReleaseRunnerPlan(t, ctx, client, project, request, profile.artifactsConfig())

	expectedRunners, err := gitLabReleaseRunnerRoles(buildTags, []string{}, profile.releaseImages, len(profile.export) != 0, profile.baseStages, plans)
	if err != nil {
		t.Fatal(err)
	}

	requireGitLabReleaseRunnerRouting(t, ctx, client, project, child,
		expectedRunners)
	requireGitLabPackageSignatures(t, target, repo, keys, profile.finalTag)
	requireGitLabPublishedImages(t, client, target, repo, publication, child, keys)
	requireGitLabPackages(t, target, repo, profile.packages, profile.finalTag)
	t.Logf("lifecycle phase 4 release: %s requested at %s, release commit %s, pipeline %d, child %d, %d verified bundles", profile.finalTag, request, released, run.ID, child, verified)

	// Phase 5: the project holds the lifecycle and nothing else.
	requireBranches(t, target, repo, map[string]string{layerMain: released, lifecycleBranch: head})
	requireGitLabLifecyclePipelines(t, ctx, client, project, released, []int64{review.ID, development.ID, run.ID}, layerMain, profile.finalTag)

	tags, _, err := client.Tags.ListTags(project, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if len(tags) != 2 {
		t.Errorf("the project has %d tags, want the request and the release", len(tags))
	}

	releases, _, err := client.Releases.ListReleases(project, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if len(releases) != 1 {
		t.Errorf("the project has %d releases, want one", len(releases))
	}

	deployKeys, _, err := client.DeployKeys.ListProjectDeployKeys(project, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if len(deployKeys) != 1 {
		t.Errorf("the project has %d deploy keys, want only the bot's", len(deployKeys))
	}

	t.Logf("lifecycle complete: base %s, change %s, merge %s, release %s; pipelines %d, %d, %d", base, head, request, released, review.ID, development.ID, run.ID)
}

// requireGitLabLifecyclePipeline ends the lifecycle at the phase whose
// pipeline did not succeed, with the failed jobs' traces, the pipeline's and
// its child pipelines'.
func requireGitLabLifecyclePipeline(t *testing.T, ctx context.Context, client *gitlabapi.Client, project, phase string, pipeline *gitlabapi.Pipeline, children ...int64) {
	t.Helper()

	if pipeline.Status == "success" {
		return
	}

	for _, id := range append([]int64{pipeline.ID}, children...) {
		logGitLabTrace(t, gitlabTrialTrace(t, ctx, client, project, id))
	}

	livetest.FailOnRunConclusionf(t, "lifecycle phase %s: pipeline %d ended %q, want success", phase, pipeline.ID, pipeline.Status)
}

// requireGitLabPipelineIDs holds the project to exactly the pipelines the
// lifecycle started: a phase starts no pipeline but its own. Child pipelines
// are the trigger jobs' and are not listed among the project's.
func requireGitLabPipelineIDs(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, want ...int64) {
	t.Helper()

	pipelines, _, err := client.Pipelines.ListProjectPipelines(project, &gitlabapi.ListProjectPipelinesOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	got := make([]int64, 0, len(pipelines))
	for _, pipeline := range pipelines {
		got = append(got, pipeline.ID)
	}

	slices.Sort(got)

	want = slices.Sorted(slices.Values(want))

	if !slices.Equal(got, want) {
		t.Fatalf("the project has pipelines %v, want exactly %v", got, want)
	}
}

// requireGitLabLifecyclePipelines holds the project, after the release, to
// the lifecycle's pipelines and the records GitLab keeps of the release
// engine's own pushes. The engine pushes the release commit to the main
// branch and the final tag with "[skip ci]" in the commit message; GitLab
// persists an empty pipeline in status "skipped" for each such push (its
// "Skip a pipeline" documentation: the pipeline is still created and reports
// skipped), where Forgejo records no run. Those two are the only pipelines
// allowed beyond the lifecycle's own, and they must be the release commit's
// on exactly those two refs.
func requireGitLabLifecyclePipelines(t *testing.T, ctx context.Context, client *gitlabapi.Client, project, released string, lifecycle []int64, skippedRefs ...string) {
	t.Helper()

	pipelines, _, err := client.Pipelines.ListProjectPipelines(project, &gitlabapi.ListProjectPipelinesOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	wantLifecycle := slices.Sorted(slices.Values(lifecycle))
	wantSkipped := slices.Sorted(slices.Values(skippedRefs))

	var gotLifecycle []int64

	var gotSkipped []string

	for _, pipeline := range pipelines {
		switch {
		case slices.Contains(wantLifecycle, pipeline.ID):
			gotLifecycle = append(gotLifecycle, pipeline.ID)
		case pipeline.Status == "skipped" && pipeline.Source == string(gitlabapi.PipelineSourcePush) && pipeline.SHA == released:
			gotSkipped = append(gotSkipped, pipeline.Ref)
		default:
			t.Fatalf("the project has pipeline %d (%s %s on %s at %s), want only the lifecycle's %v and the release commit's skipped pushes on %v",
				pipeline.ID, pipeline.Status, pipeline.Source, pipeline.Ref, pipeline.SHA, wantLifecycle, wantSkipped)
		}
	}

	slices.Sort(gotLifecycle)
	slices.Sort(gotSkipped)

	if !slices.Equal(gotLifecycle, wantLifecycle) {
		t.Fatalf("the project has lifecycle pipelines %v, want exactly %v", gotLifecycle, wantLifecycle)
	}

	if !slices.Equal(gotSkipped, wantSkipped) {
		t.Fatalf("the project has skipped push pipelines for release commit %s on %v, want exactly %v", released, gotSkipped, wantSkipped)
	}
}

// requireGitLabNoReleases holds the project to no tag and no release before
// the release phase.
func requireGitLabNoReleases(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string) {
	t.Helper()

	tags, _, err := client.Tags.ListTags(project, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	releases, _, err := client.Releases.ListReleases(project, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if len(tags) != 0 || len(releases) != 0 {
		t.Fatalf("the project has %d tags and %d releases before the release phase", len(tags), len(releases))
	}
}

// requireGitLabServiceSnapshot reads what the snapshot component left: the
// frozen plan the parent's plan job holds and the receipts the child's
// collect job holds, then judges them as the Forgejo service development's
// are judged. The readiness and checked-JAR evidence are the Forgejo
// workflow's own jobs; the component has none, so none is claimed here.
func requireGitLabServiceSnapshot(t *testing.T, ctx context.Context, client *gitlabapi.Client, target livetest.Target, repo string, consumer livetest.Consumer, parent, child int64, head, image string) (snapshotimage.Receipt, snapshotimage.Plan) {
	t.Helper()

	plan := gitlabJobArtifactFile(t, ctx, client, target, repo, parent, gitlabSnapshotPlanJob, "snapshot-plan.json")
	receipts := gitlabJobArtifactFile(t, ctx, client, target, repo, child, gitlabSnapshotCollectJob, "container-images.json")

	return requireServiceSnapshot(ctx, t, target, repo, consumer, lifecycleBranch, head, image, plan, receipts)
}

// gitlabJobArtifactFile reads one file of the artifacts of the named job of
// a pipeline, which must have succeeded once.
func gitlabJobArtifactFile(t *testing.T, ctx context.Context, client *gitlabapi.Client, target livetest.Target, repo string, pipeline int64, job, path string) []byte {
	t.Helper()

	project := livetest.RepoSlug(target, repo)
	id := gitlabSuccessfulJob(t, ctx, client, project, pipeline, job)

	address := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/artifacts/%s", target.BaseURL(), url.PathEscape(project), id, path)

	return livetest.DownloadContext(ctx, t, target, address, 1<<20)
}

// gitlabJobArtifacts reads the whole artifacts archive of the named job of a
// pipeline, which must have succeeded once, as the files it holds by path.
func gitlabJobArtifacts(t *testing.T, ctx context.Context, client *gitlabapi.Client, target livetest.Target, repo string, pipeline int64, job string) map[string][]byte {
	t.Helper()

	project := livetest.RepoSlug(target, repo)
	id := gitlabSuccessfulJob(t, ctx, client, project, pipeline, job)

	address := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/artifacts", target.BaseURL(), url.PathEscape(project), id)

	tree, err := livetest.ZIPFiles(livetest.DownloadContext(ctx, t, target, address, 256<<20), 256<<20)
	if err != nil {
		t.Fatalf("the artifacts of %s in pipeline %d: %v", job, pipeline, err)
	}

	return tree
}

// gitlabSuccessfulJob is the id of the named job of a pipeline, which must
// have succeeded once.
func gitlabSuccessfulJob(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, pipeline int64, job string) int64 {
	t.Helper()

	jobs, response, err := client.Jobs.ListPipelineJobs(project, pipeline, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if response == nil || response.NextPage != 0 {
		t.Fatalf("reading %s of pipeline %d requires a complete job listing", job, pipeline)
	}

	var id int64

	for _, candidate := range jobs {
		if candidate.Name != job || candidate.Status != "success" {
			continue
		}

		if id != 0 {
			t.Fatalf("pipeline %d ran %s twice", pipeline, job)
		}

		id = candidate.ID
	}

	if id == 0 {
		t.Fatalf("pipeline %d has no successful %s job", pipeline, job)
	}

	return id
}
