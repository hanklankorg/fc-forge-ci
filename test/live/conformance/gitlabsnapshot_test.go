// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"maps"
	"net/http"
	"os"
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

// gitlabSnapshotPolicyFile is the one policy file of a snapshot's project.
const gitlabSnapshotPolicyFile = "snapshot.yml"

// gitlabRequestedMainRule admits an include for the pipeline the harness
// requests on main through the API alone, no push's: a snapshot is requested,
// not the effect of a change the harness makes.
const gitlabRequestedMainRule = `$CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_NAME == "main"`

// A snapshot on GitLab: the lifecycle's development phase alone, in
// one GitLab project under one pinned policy that includes the
// snapshot-images component and nothing else, over every image the planner
// plans from the consumer's declaration (docs/live-runs.md, Snapshot).
// The consumer's main and a change on branch development are pushed before
// the policy is in place, and no rule of it admits a push; the harness then
// requests one pipeline on main, whose child snapshots development, and
// observes what it left: the frozen plan in the parent's plan job, every
// planned image's receipt in the child's collect job, the registry's tags
// (requireSnapshotImages), and a project with that pipeline, its two
// branches unmoved and nothing released. The component probes no readiness,
// so none is claimed. The development registry credential is a protected
// variable scoped to the publisher's environment, on a protected main.
//
// A consumer whose declaration has no container is snapshotted for its file
// outputs instead: the policy names no image repository and asks for the
// files, the project holds no registry variable, and the trial observes the
// frozen plan and the bundle each child files job kept
// (requireGitLabSnapshotFiles), in a project with no package.
func TestGitLabLayer_Snapshot(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	forEachConsumerCheckout(t, provider.ForgeGitLab, "just test-live-consumer gitlab <checkout> snapshot job-archive=<path>", func(t *testing.T, root string) {
		t.Helper()

		if livetest.DemoName() != "" {
			t.Fatal("a snapshot runs in disposable scratch projects, not in a kept demo")
		}

		runGitLabSnapshot(t, root)
	}, livetest.TrialSnapshot)
}

// gitlabSnapshotRow is what the visibilities of one snapshot row share: the
// declaration, the selected builder, its archive, and the job images pushed
// for the row.
type gitlabSnapshotRow struct {
	root, source, builderArchive string
	builder                      livetest.ImageBuilderChoice
	jobImage, builderJobImage    string
	pushed                       livetest.JobImages
}

// runGitLabSnapshot runs the snapshot once for each visibility. The job
// image and the Buildah job image are pushed once, before them, as a
// release's are (runGitLabReleaseScenario). Each visibility serves a layer of
// its own, and pushes beside it a kaniko job image, labelled with that layer,
// and the producer images of the families its plan names that are not pushed
// already.
func runGitLabSnapshot(t *testing.T, root string) {
	t.Helper()

	visibilities := livetest.ConsumerVisibilities(t)

	if event := os.Getenv(consumerEventEnv); event != livetest.TrialSnapshot {
		t.Fatalf("the event is %s; this trial runs %s", event, livetest.TrialSnapshot)
	}

	// The declaration the consumer's release builds its images from, unless
	// the trial names a variant of it (snapshot-declaration=).
	source, err := livetest.GitLabSnapshotDeclaration(strings.TrimSpace(os.Getenv(consumerSettingsEnv)))
	if err != nil {
		t.Fatal(err)
	}

	if source == "" {
		if source, err = livetest.ServiceSnapshotSource(root); err != nil {
			t.Fatal(err)
		}
	}

	jobArchive := os.Getenv("FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE")
	if jobArchive == "" {
		livetest.NotCoveredf(t, "a GitLab snapshot requires the reviewed job image archive of job-archive=")
	}

	archiveEnv := livetest.GitLabSnapshotBuilderArchiveEnv(livetest.SelectedImageBuilder(t, provider.ForgeGitLab))

	builderArchive := os.Getenv(archiveEnv)
	if builderArchive == "" {
		livetest.NotCoveredf(t, "GitLab snapshot images require %s, a saved builder job image", archiveEnv)
	}

	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "a snapshot needs a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	builder := livetest.ChooseImageBuilder(t, target)

	pushed := livetest.JobImages{}
	push := func(scenario, archive string) string {
		return pushed.Push(t, target, archive, func() string {
			return gitlabReleaseRepo(t, target, scenario, livetest.ReadJobImageAnonymously)
		})
	}

	row := gitlabSnapshotRow{
		root: root, source: source, builderArchive: builderArchive, builder: builder,
		jobImage: push("snapshot-job-image", jobArchive), pushed: pushed,
	}
	if builder.Builder != livetest.ImageBuilderKaniko {
		row.builderJobImage = push("snapshot-builder-job-image", builderArchive)
	}

	for _, visibility := range visibilities {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabSnapshotVisibility(t, row, visibility.Private)
		})
	}
}

func runGitLabSnapshotVisibility(t *testing.T, row gitlabSnapshotRow, private bool) {
	t.Helper()

	root, source, builder := row.root, row.source, row.builder
	jobImage, builderJobImage := row.jobImage, row.builderJobImage

	target := livetest.Accept(t, provider.ForgeGitLab)

	layer := livetest.PushLayer(t, target, gitlabReleaseRepo(t, target, "snapshot-engine", livetest.ReadLayerAnonymously))
	consumer := livetest.OpenConsumer(t, target, root).WithDraftLayer(t, layer).WithSnapshotImagesDeclaration(t, source)
	families := livetest.ServiceSnapshotFamilies(livetest.Context(t), t, consumer)

	// A declaration without a container plans file outputs alone, and its
	// snapshot is given no registry credential and no image repository.
	files := len(livetest.SnapshotFileOutputs(livetest.Context(t), t, consumer)) != 0

	// A kaniko job image is labelled with the layer it runs from, which
	// admits it.
	if builder.Builder == livetest.ImageBuilderKaniko {
		builderJobImage = livetest.PushKanikoJobImage(t, target, gitlabReleaseRepo(t, target, "snapshot-builder-job-image", livetest.ReadJobImageAnonymously), row.builderArchive, layer)
	}

	// Each producer builds in the runtime image of its toolchain family,
	// often the archive of the job image too.
	producerImages := map[string]string{}
	pushed := maps.Clone(row.pushed)

	for _, family := range families {
		archive := os.Getenv(livetest.GitLabSnapshotProducerArchiveEnv(family))
		if archive == "" {
			livetest.NotCoveredf(t, "the snapshot builds a %s producer; set %s to the %s runtime image archive", family, livetest.GitLabSnapshotProducerArchiveEnv(family), family)
		}

		producerImages[family] = pushed.Push(t, target, archive, func() string {
			return gitlabReleaseRepo(t, target, "snapshot-producer-image-"+family, livetest.ReadJobImageAnonymously)
		})
	}

	policyRepo := gitlabReleaseRepo(t, target, "snapshot-policy", "")
	policyProject := livetest.RepoSlug(target, policyRepo)
	repo := livetest.NewBareScratchRepo(t, target, livetest.SnapshotScenario, consumer.ObjectFormat, private)
	project := livetest.RepoSlug(target, repo)

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(livetest.Context(t), 60*time.Minute)
	defer cancel()

	livetest.RetainGitLabJobLogsAfter(t, target, repo, map[string]string{"SNAPSHOT_REGISTRY_PASSWORD": target.Token})

	if !files {
		createGitLabProtectedVariables(t, client, project, []gitlabProjectVariable{
			{"SNAPSHOT_REGISTRY_USER", target.CredentialUsername, "snapshot/registry"},
			{"SNAPSHOT_REGISTRY_PASSWORD", target.Token, "snapshot/registry"},
		})
	}

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	image := registry + "/" + strings.ToLower(project)

	buildTags := builder.RunnerLabels

	inputs := map[string]any{
		"branch": lifecycleBranch, "artifacts-config": livetest.ServiceSnapshotDeclarationFile,
		"image-repository": image, "forge-ci-repository": layer.Repository,
		"server-url": target.BaseURL(), "registry": registry,
		"job-image": jobImage, "build-image": builderJobImage,
		"image-builder": builder.Builder, "builder-job-image": builderJobImage,
		"runner-tags": []string{}, "build-runner-tags-linux-amd64": buildTags, "build-runner-tags-linux-arm64": []string{},
	}

	for family, producerImage := range producerImages {
		inputs["artifact-job-image-"+family] = producerImage
	}

	if files {
		delete(inputs, "image-repository")

		inputs["publish-files"] = true
	}

	policyBody, err := yaml.Marshal(map[string]any{"include": []any{map[string]any{
		"component": gitlabComponent(target, layer, "snapshot-images"),
		"inputs":    layer.ComponentAdminInputs(t, "snapshot-images", inputs),
		"rules":     []any{map[string]string{"if": gitlabRequestedMainRule}},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	policy := gitlabPolicyCommit(t, ctx, client, policyProject, gitlabSnapshotPolicyFile, string(policyBody), gitlabapi.FileCreate)

	livetest.RestrictGitLabJobToken(t, target, repo)

	t.Logf("consumer %s at %s (%s); layer %s at %s; job image %s, producer images %v of %s, image builder %s", root, consumer.Commit, consumer.ObjectFormat, layer.Repository, layer.Commit, jobImage, producerImages, source, builder.Builder)

	base := consumer.PushSnapshot(t, repo)
	requireObjectFormats(t, consumer.ObjectFormat, base, layer.Commit)

	head := consumer.PushChange(t, repo, lifecycleBranch, base)

	useGitLabAdminPolicy(ctx, t, client, project, gitlabSnapshotPolicyFile+"@"+policyProject+":"+policy.ID)
	waitGitLabPush(t, ctx, client, project, lifecycleBranch, head)
	protectGitLabMain(ctx, t, client, project)

	requested, _, err := client.Pipelines.CreatePipeline(project, &gitlabapi.CreatePipelineOptions{Ref: new(layerMain)}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatalf("request the snapshot pipeline on %s: %v", layerMain, err)
	}

	run := waitGitLabRequestedPipeline(t, ctx, client, project, requested.ID, base)
	child := requireGitLabChildPipeline(t, ctx, client, project, run)

	if run.Status != "success" {
		for _, id := range []int64{run.ID, child} {
			logGitLabTrace(t, gitlabTrialTrace(t, ctx, client, project, id))
		}

		livetest.FailOnRunConclusionf(t, "snapshot pipeline %d ended %q, want success", run.ID, run.Status)
	}

	plan := gitlabJobArtifactFile(t, ctx, client, target, repo, run.ID, gitlabSnapshotPlanJob, "snapshot-plan.json")

	if files {
		kept := requireGitLabSnapshotFiles(t, ctx, client, target, repo, consumer, child, head, plan)

		requireGitLabPipelineIDs(t, ctx, client, project, run.ID)
		requireGitLabNoReleases(t, ctx, client, project)
		requireGitLabNoPackages(t, ctx, client, project)
		requireBranches(t, target, repo, map[string]string{layerMain: base, lifecycleBranch: head})

		t.Logf("snapshot: pipeline %d requested on %s@%s, child %d, snapshots %s@%s as the unsigned file bundles of jobs %s; no image, no package", run.ID, layerMain, base, child, lifecycleBranch, head, strings.Join(kept, ", "))

		return
	}

	receipts := gitlabJobArtifactFile(t, ctx, client, target, repo, child, gitlabSnapshotCollectJob, "container-images.json")
	published, expected := requireSnapshotImages(ctx, t, target, repo, consumer, lifecycleBranch, head, image, plan, receipts)

	requireGitLabSnapshotEvidence(t, ctx, client, target, repo, child, expected, published)

	requireGitLabPipelineIDs(t, ctx, client, project, run.ID)
	requireGitLabNoReleases(t, ctx, client, project)
	requireBranches(t, target, repo, map[string]string{layerMain: base, lifecycleBranch: head})

	refs := make([]string, 0, len(published))
	for _, receipt := range published {
		refs = append(refs, receipt.Name+"="+receipt.Ref)
	}

	t.Logf("snapshot: pipeline %d requested on %s@%s, child %d, snapshots %s@%s as %s; producers %d", run.ID, layerMain, base, child, lifecycleBranch, head, strings.Join(refs, " "), len(expected.Artifacts))
}

// requireGitLabSnapshotFiles reads what a snapshot of file outputs left in
// its child pipeline: the frozen plan, which plans file outputs and no image
// (livetest.RequireSnapshotFilesPlan), and the artifacts of each planned
// output's job, Snapshot: files <name>, whose tree below
// snapshot-files/<name>/ is the bundle the oracle judges
// (livetest.SnapshotFilesFindings); a file the job kept outside that
// directory stays under its whole path, where no receipt lists it. It returns
// the names of the jobs it read, in plan order.
func requireGitLabSnapshotFiles(t *testing.T, ctx context.Context, client *gitlabapi.Client, target livetest.Target, repo string, consumer livetest.Consumer, child int64, head string, plan []byte) []string {
	t.Helper()

	expected := livetest.RequireSnapshotFilesPlan(t, livetest.SnapshotRun{
		Declaration: livetest.ServiceSnapshotDeclaration(ctx, t, consumer), Branch: lifecycleBranch, Head: head, Plan: plan,
	})

	bundles := map[string]map[string][]byte{}
	jobs := make([]string, 0, len(expected.Files))

	for _, file := range expected.Files {
		job := "Snapshot: files " + file.Name
		bundle := map[string][]byte{}

		for path, content := range gitlabJobArtifacts(t, ctx, client, target, repo, child, job) {
			bundle[strings.TrimPrefix(path, "snapshot-files/"+file.Name+"/")] = content
		}

		bundles[file.Name] = bundle

		jobs = append(jobs, job)
	}

	if findings := livetest.SnapshotFilesFindings(expected, bundles); len(findings) != 0 {
		t.Fatalf("child pipeline %d did not keep the file outputs its plan names:\n%s", child, strings.Join(findings, "\n"))
	}

	return jobs
}

// requireGitLabSnapshotEvidence holds the evidence each planned image's
// publishing job retained, the artifacts of Snapshot: publish <image> below
// snapshot-evidence/<image>/, to the declaration's SBOM policy as the
// expected plan carries it (livetest.SnapshotEvidenceFindings): the image's
// scan, and an SBOM exactly where the plan generates one. A file the job
// kept elsewhere below snapshot-evidence/ stays under its whole path, where
// the oracle admits none; the job's receipt is not evidence.
func requireGitLabSnapshotEvidence(t *testing.T, ctx context.Context, client *gitlabapi.Client, target livetest.Target, repo string, child int64, expected snapshotimage.Plan, receipts []snapshotimage.Receipt) {
	t.Helper()

	const root = "snapshot-evidence/"

	evidenced := map[string]map[string][]byte{}

	for _, planned := range expected.Images {
		tree := map[string][]byte{}

		for path, content := range gitlabJobArtifacts(t, ctx, client, target, repo, child, "Snapshot: publish "+planned.Name) {
			if strings.HasPrefix(path, root) {
				tree[strings.TrimPrefix(path, root+planned.Name+"/")] = content
			}
		}

		evidenced[planned.Name] = tree

		t.Logf("snapshot evidence of image %s: the declaration plans its SBOM %t, and the job retained %v", planned.Name, planned.SBOM, slices.Sorted(maps.Keys(tree)))
	}

	if findings := livetest.SnapshotEvidenceFindings(expected, receipts, evidenced); len(findings) != 0 {
		t.Fatalf("the snapshot's retained evidence does not keep the declaration's SBOM policy:\n%s", strings.Join(findings, "\n"))
	}
}

// requireGitLabNoPackages holds the project to no package and no container
// repository: a snapshot of files publishes to neither registry.
func requireGitLabNoPackages(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string) {
	t.Helper()

	packages, _, err := client.Packages.ListProjectPackages(project, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	repositories, _, err := client.ContainerRegistry.ListProjectRegistryRepositories(project, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if len(packages) != 0 || len(repositories) != 0 {
		t.Fatalf("the project has %d packages and %d container repositories after a snapshot of files, want none", len(packages), len(repositories))
	}
}

// protectGitLabMain protects main, the ref the requested pipeline runs on,
// so the protected development registry credential reaches its publisher:
// GitLab may already have protected the default branch after the first push,
// which leaves it protected all the same.
func protectGitLabMain(ctx context.Context, t *testing.T, client *gitlabapi.Client, project string) {
	t.Helper()

	_, response, err := client.ProtectedBranches.ProtectRepositoryBranches(project, &gitlabapi.ProtectRepositoryBranchesOptions{
		Name: new(layerMain), AllowForcePush: new(false),
	}, gitlabapi.WithContext(ctx))
	if err != nil && (response == nil || response.StatusCode != http.StatusConflict) {
		t.Fatalf("protect %s: %v", layerMain, err)
	}
}

// waitGitLabRequestedPipeline waits for the pipeline the harness requested
// through the API to finish, bound to that request, the ref main and its
// exact commit, and logs GitLab's account of one that did not succeed.
func waitGitLabRequestedPipeline(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, id int64, sha string) *gitlabapi.Pipeline {
	t.Helper()

	started := time.Now()

	for {
		run, err := livetest.PollGitLabPipeline(ctx, client, project, id)
		if err != nil {
			t.Fatal(err)
		}

		if run.SHA != sha || run.Source != "api" || run.Ref != layerMain {
			t.Fatal("the pipeline is not bound to the harness's request, main and its exact commit")
		}

		switch run.Status {
		case "success", "failed", "canceled", "skipped", "manual":
			livetest.LogGitLabPipelineFailure(t, ctx, client, project, run)
			t.Logf("GitLab requested pipeline %d: %s after %s (%s)", run.ID, run.Status, time.Since(started).Round(time.Second), run.WebURL)

			return run
		}

		select {
		case <-ctx.Done():
			t.Fatal("the requested pipeline did not finish within its bounded wait")
		case <-time.After(2 * time.Second):
		}
	}
}
