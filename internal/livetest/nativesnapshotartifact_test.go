// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	gitlabapi "gitlab.com/gitlab-org/api/client-go"
)

// Interrupted controllers may not run testing cleanup hooks. This explicit
// recovery case accepts only externally acknowledged exact names/creation IDs;
// the existing SDK still enforces its compiled namespace and provider guards.
func TestSnapshotNativeExactCleanup(t *testing.T) {
	input := os.Getenv("FORGE_CI_LIVE_SNAPSHOT_CLEANUP_ACK")
	if input == "" {
		t.Skip("explicit exact-resource cleanup acknowledgement required")
	}

	var acknowledged []struct {
		Name         string `json:"name"`
		ID           int64  `json:"id"`
		RunID        int64  `json:"run_id"`
		EngineSHA256 string `json:"engine_sha256"`
	}
	if err := json.Unmarshal([]byte(input), &acknowledged); err != nil || len(acknowledged) == 0 {
		t.Fatal("invalid exact-resource acknowledgement")
	}

	target := Accept(t, provider.ForgeForgejo)

	ctx, cancel := context.WithTimeout(Context(t), 3*time.Minute)
	defer cancel()

	for _, resource := range acknowledged {
		if resource.ID <= 0 || !inRunNamespace(target, resource.Name) {
			t.Fatal("cleanup coordinate is not an acknowledged owned repository")
		}

		var current struct {
			ID int64 `json:"id"`
		}
		if _, err := decode(ctx, target, http.MethodGet, actionsURL(target, resource.Name, ""), nil, &current, http.StatusOK); err != nil || current.ID != resource.ID {
			t.Fatal("cleanup immutable repository identity mismatch")
		}

		if resource.RunID > 0 {
			for _, job := range RunJobs(t, target, resource.Name, resource.RunID) {
				if job.Status != "success" && job.Status != "failure" && job.Status != "cancelled" && job.Status != "skipped" {
					t.Fatal("owned snapshot job remains active")
				}
			}
		}

		if resource.EngineSHA256 != "" {
			DeletePublishedPackage(t, target, resource.Name, "generic", layerPackage, resource.EngineSHA256)
		}

		if err := DeleteScratchRepo(ctx, target, resource.Name); err != nil {
			t.Fatal(err)
		}

		t.Logf("exact snapshot cleanup completed: repository=%s id=%d", RepoSlug(target, resource.Name), resource.ID)
	}
}

const snapshotNativeToolsImage = "ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:450b351477a8cf8ea9f5a58d704d24a046162dc696f452bfc6190de25e80b05e"

func TestGitLabSnapshotBuilderCapability(t *testing.T) {
	target := Accept(t, provider.ForgeGitLab)
	label := PrivilegedRunnerLabel(t, target)

	if label == "" || RunnerArch(t, target) != "amd64" {
		t.Fatal("advertised native amd64 privileged runner required")
	}

	repo := NewBareScratchRepo(t, target, "snapshot-builder-probe", "sha1", false)
	files := snapshotCandidateFiles(t)
	sha, _, _ := nativeSnapshotTree(t, target, repo, files)
	nativeSnapshotPublishEngine(t, target, repo)

	tags, err := json.Marshal([]string{label})
	if err != nil {
		t.Fatal(err)
	}

	workflow := fmt.Sprintf(`probe:
  tags: %s
  image:
    name: %s
    entrypoint: ['']
  variables:
    GIT_STRATEGY: none
  script:
    - |
      set -euo pipefail
      test "$(uname -m)" = x86_64
      export CI_OUTPUT="${CI_PROJECT_DIR}/probe-outputs.env"
      if [ -n "${CI_SERVER_TLS_CA_FILE:-}" ]; then
        cat /etc/ssl/certs/ca-certificates.crt "${CI_SERVER_TLS_CA_FILE}" > "${CI_PROJECT_DIR}/probe-ca.crt"
        export GIT_SSL_CAINFO="${CI_PROJECT_DIR}/probe-ca.crt" CURL_CA_BUNDLE="${CI_PROJECT_DIR}/probe-ca.crt" SSL_CERT_FILE="${CI_PROJECT_DIR}/probe-ca.crt"
      fi
      git clone --quiet "$CI_SERVER_URL/$CI_PROJECT_PATH.git" helpers
      git -C helpers checkout --quiet --detach %s
      export FORGE_CI_PACKAGE_PROVIDER=gitlab
      export FORGE_CI_REPOSITORY_URL="$CI_SERVER_URL/$CI_PROJECT_PATH.git"
      source helpers/.engine/bin/lib-forge-ci.sh
      fetch_forge_ci
      run_forge_ci container setup-buildah --install-packages=true --probe-build=true --print-store=false --summary=false
      printf 'NATIVE_AMD64_LAYERED_BUILDER_PASSED\n'
`, string(tags), snapshotNativeToolsImage, sha)
	if status := RunWorkflow(t, target, repo, "snapshot-builder-probe", workflow); status != "success" {
		t.Fatalf("native snapshot builder capability failed: %s", status)
	}

	t.Logf("native GitLab snapshot builder passed: repository=%s pinned-helper=%s image=%s runner=%s", RepoSlug(target, repo), sha, snapshotNativeToolsImage, label)
}

func TestForgejoSnapshotArtifactFirst(t *testing.T) {
	if !Requires(t, provider.ForgeForgejo, NeedsInRunner) {
		NotCoveredf(t, "native Forgejo runner unavailable")
	}

	target := Accept(t, provider.ForgeForgejo)
	if RunnerArch(t, target) != "amd64" {
		t.Fatal("this focused snapshot smoke requires native amd64")
	}

	engine := NewBareScratchRepo(t, target, "snapshot-engine", "sha1", false)
	consumer := NewBareScratchRepo(t, target, "snapshot-artifact", "sha1", false)
	engineSHA, _, _ := nativeSnapshotTree(t, target, engine, snapshotCandidateFiles(t))
	nativeSnapshotPublishEngine(t, target, engine)

	privileged := PrivilegedRunnerLabel(t, target)
	if privileged == "" {
		t.Fatal("native Buildah snapshot acceptance requires the advertised privileged runner")
	}
	label, err := json.Marshal([]string{privileged})
	if err != nil {
		t.Fatal(err)
	}

	workflow := strings.NewReplacer("ENGINE_REPOSITORY", RepoSlug(target, engine), "ENGINE_SHA", engineSHA, "GO_IMAGE", snapshotNativeToolsImage).Replace(`name: Native artifact first snapshot
on:
  workflow_dispatch:
permissions:
  contents: read
jobs:
  snapshot:
    uses: ENGINE_REPOSITORY/.forgejo/workflows/snapshot-images.yml@ENGINE_SHA
    with:
      branch: feature/artifact-source
      forge-ci-repository: ENGINE_REPOSITORY
      forge-ci-sha: ENGINE_SHA
      runner-label: '${{ vars.CI_RUNNER_LABEL }}'
      artifact-job-image-go: GO_IMAGE
      image-repository: IMAGE_REPOSITORY
      registry-user: REGISTRY_USER
    secrets:
      SNAPSHOT_REGISTRY_PASSWORD: ${{ secrets.SNAPSHOT_REGISTRY_PASSWORD }}
`)

	registry, err := RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	imageRepository := registry + "/" + strings.ToLower(RepoSlug(target, consumer))
	workflow = strings.NewReplacer("IMAGE_REPOSITORY", imageRepository, "REGISTRY_USER", target.CredentialUsername).Replace(workflow)

	files := snapshotCanaryFiles(false)
	files[".forgejo/workflows/snapshot-native.yml"] = filePayload{Body: workflow, Mode: 0644}
	policy, dir, git := nativeSnapshotTree(t, target, consumer, files)
	source := nativeSnapshotSelected(t, target, consumer, dir, git)
	EnableActions(t, target, consumer)
	SetRepoVariable(t, target, consumer, "CI_RUNNER_LABEL", string(label))
	SetRepoSecret(t, target, consumer, "SNAPSHOT_REGISTRY_PASSWORD", target.Token)

	ctx, cancel := context.WithTimeout(Context(t), time.Minute)
	defer cancel()

	endpoint := actionsURL(target, consumer, "/actions/workflows/snapshot-native.yml/dispatches")
	if err := discard(ctx, target, http.MethodPost, endpoint, map[string]string{"ref": "main"}, http.StatusNoContent); err != nil {
		t.Fatalf("dispatch exact native snapshot: %v", err)
	}

	run := waitSnapshotDispatch(t, target, consumer, policy)

	jobs := RunJobs(t, target, consumer, run.ID)
	for _, job := range jobs {
		t.Logf("native snapshot job: forge=%s run=%d job=%d name=%s status=%s", target.Forge, run.ID, job.ID, job.Name, job.Status)
	}

	if run.Status != "success" {
		logWorkflowFailure(Context(t), t, target, consumer, "snapshot-native")
		t.Fatalf("Forgejo snapshot run %d ended %s", run.ID, run.Status)
	}

	trees := map[string]map[string][]byte{}

	for _, artifact := range RunArtifacts(t, target, consumer, run.ID) {
		if artifact.Name != "snapshot-images" && artifact.Name != "snapshot-images-plan" && artifact.Name != "snapshot-images-context" {
			continue
		}

		body := ArtifactZIP(t, target, consumer, artifact.ID)

		tree, err := ZIPTree(body, 64<<20)
		if err != nil {
			t.Fatal(err)
		}

		trees[artifact.Name] = tree
		saveSnapshotEvidence(t, "forgejo-"+artifact.Name+".zip", body)
	}

	receipt, file, err := checkSnapshotArtifactEvidence(trees["snapshot-images-plan"]["snapshot-plan.json"], trees["snapshot-images"]["container-images.json"], trees["snapshot-images-context"]["files/snapshot-dependencies.json"], source, imageRepository)
	if err != nil {
		t.Fatal(err)
	}

	options, err := registryRemoteOptions(target)
	if err != nil {
		t.Fatal(err)
	}

	snapshotRegistryCanary(t, t.Context(), receipt.Ref, receipt.Tag, target.CredentialUsername, target.Token, file.SHA256, source, options...)
	t.Logf("native snapshot accepted: forge=forgejo run=%d candidate=%s policy=%s source=%s ref=%s tag=%s mode=%s sha256=%s", run.ID, engineSHA, policy, source, receipt.Ref, receipt.Tag, file.Mode, file.SHA256)
}

func waitSnapshotDispatch(t *testing.T, target Target, repo, source string) WorkflowRun {
	t.Helper()

	ctx, cancel := context.WithTimeout(Context(t), 20*time.Minute)
	defer cancel()

	var observed WorkflowRun
	defer func() {
		if observed.ID == 0 {
			return
		}

		cleanup, closeCleanup := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer closeCleanup()

		if err := cancelObservedWorkflowRun(cleanup, target, repo, observed); err != nil {
			t.Error("snapshot dispatch cleanup failed")
		}
	}()

	for {
		var payload struct {
			Runs []struct {
				WorkflowRun

				Commit     string `json:"commit_sha"`
				Repository struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
			} `json:"workflow_runs"`
		}
		if _, err := decodeQuiet(ctx, target, actionsURL(target, repo, "/actions/runs"), &payload); err == nil {
			for _, run := range payload.Runs {
				if run.WorkflowID != "snapshot-native.yml" || run.Commit != source {
					continue
				}

				observed = run.WorkflowRun
				if run.Repository.FullName != RepoSlug(target, repo) || run.TriggerEvent != "workflow_dispatch" || (run.Event != "" && run.Event != "workflow_dispatch") {
					t.Fatal("snapshot dispatch identity mismatch")
				}

				if runIsOver(run.Status) {
					return run.WorkflowRun
				}
			}
		}

		select {
		case <-ctx.Done():
			t.Fatal("snapshot dispatch wait deadline")
		case <-time.After(2 * time.Second):
		}
	}
}

func nativeSnapshotTree(t *testing.T, target Target, repo string, files map[string]filePayload) (string, string, layerGit) {
	t.Helper()

	dir := t.TempDir()
	snapshotWriteFiles(t, dir, files)

	gitBase := strings.TrimSuffix(target.endpoint.GitURL(), "/")
	git := layerGit{target: target, env: ToolEnvironment(t, target, layerGitEnvironment(gitBase, target.CredentialUsername, target.Token))}

	ctx, cancel := context.WithTimeout(Context(t), 5*time.Minute)
	defer cancel()

	for _, args := range [][]string{{"init", "--quiet", "--object-format=sha1", "--initial-branch=main"}, {"add", "--all"}, {"-c", "user.name=Snapshot Fixture", "-c", "user.email=snapshot@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "test: frozen working-tree snapshot fixture"}} {
		if _, err := git.run(ctx, dir, args...); err != nil {
			t.Fatal(err)
		}
	}

	sha, err := git.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	tree, err := git.run(ctx, dir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}

	if err := git.push(ctx, dir, scratchRemote(target, repo), sha, "main"); err != nil {
		t.Fatal(err)
	}

	t.Logf("native snapshot tree: forge=%s repository=%s commit=%s tree=%s", target.Forge, RepoSlug(target, repo), sha, tree)

	return sha, dir, git
}

func nativeSnapshotSelected(t *testing.T, target Target, repo, dir string, git layerGit) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(Context(t), 2*time.Minute)
	defer cancel()

	if _, err := git.run(ctx, dir, "checkout", "--quiet", "-b", snapshotArtifactBranch); err != nil {
		t.Fatal(err)
	}

	snapshotWriteFiles(t, dir, snapshotCanaryFiles(true))

	if _, err := git.run(ctx, dir, "add", "--all"); err != nil {
		t.Fatal(err)
	}

	if _, err := git.run(ctx, dir, "-c", "user.name=Snapshot Fixture", "-c", "user.email=snapshot@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "test: selected source differs from policy"); err != nil {
		t.Fatal(err)
	}

	sha, err := git.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	if err := git.push(ctx, dir, scratchRemote(target, repo), sha, snapshotArtifactBranch); err != nil {
		t.Fatal(err)
	}

	return sha
}

func nativeSnapshotPublishEngine(t *testing.T, target Target, repo string) {
	t.Helper()

	digest, binary := readLayerBinary(t, reporoot.Path(t))

	ctx, cancel := context.WithTimeout(Context(t), 3*time.Minute)
	defer cancel()

	if err := publishLayerBinary(ctx, target, repo, digest, binary); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { DeletePublishedPackage(t, target, repo, "generic", layerPackage, digest) })
	t.Logf("native candidate engine staged: forge=%s sha256=%s repository=%s", target.Forge, digest, RepoSlug(target, repo))
}

func TestGitLabSnapshotArtifactFirst(t *testing.T) {
	if !Requires(t, provider.ForgeGitLab, NeedsInRunner) {
		NotCoveredf(t, "native GitLab runner unavailable")
	}

	target := Accept(t, provider.ForgeGitLab)
	if RunnerArch(t, target) != "amd64" {
		t.Fatal("focused GitLab snapshot smoke requires native amd64")
	}

	privileged := PrivilegedRunnerLabel(t, target)
	if privileged == "" {
		NotCoveredf(t, "existing native Buildah-capable GitLab runner unavailable")
	}

	engine := NewBareScratchRepo(t, target, "snapshot-engine", "sha1", false)
	consumer := NewBareScratchRepo(t, target, "snapshot-artifact", "sha1", false)
	engineSHA, _, _ := nativeSnapshotTree(t, target, engine, snapshotCandidateFiles(t))
	nativeSnapshotPublishEngine(t, target, engine)

	registry, err := RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	imageRepository := registry + "/" + strings.ToLower(RepoSlug(target, consumer))

	tagJSON, err := json.Marshal([]string{privileged})
	if err != nil {
		t.Fatal(err)
	}

	workflow := strings.NewReplacer("ENGINE_REPOSITORY", RepoSlug(target, engine), "ENGINE_SHA", engineSHA, "FORGE_HOST", strings.TrimPrefix(target.BaseURL(), "https://"), "TOOLS_IMAGE", snapshotNativeToolsImage, "IMAGE_REPOSITORY", imageRepository, "BUILD_TAGS", string(tagJSON)).Replace(`workflow:
  rules:
    - if: '$CI_PIPELINE_SOURCE == "api"'
stages: [deploy]
include:
  - component: FORGE_HOST/ENGINE_REPOSITORY/snapshot-images@ENGINE_SHA
    inputs:
      branch: feature/artifact-source
      image-repository: IMAGE_REPOSITORY
      forge-ci-repository: ENGINE_REPOSITORY
      job-image: TOOLS_IMAGE
      build-image: TOOLS_IMAGE
      artifact-job-image-go: TOOLS_IMAGE
      build-runner-tags: BUILD_TAGS
`)
	files := snapshotCanaryFiles(false)
	files[".gitlab-ci.yml"] = filePayload{Body: workflow, Mode: 0644}
	policy, dir, git := nativeSnapshotTree(t, target, consumer, files)
	source := nativeSnapshotSelected(t, target, consumer, dir, git)

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"), gitlabapi.WithHTTPClient(HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	project := RepoSlug(target, consumer)

	run, _, err := client.Pipelines.CreatePipeline(project, &gitlabapi.CreatePipelineOptions{Ref: new("main")}, gitlabapi.WithContext(t.Context()))
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("native snapshot pipeline: forge=gitlab pipeline=%d url=%s policy=%s source=%s", run.ID, run.WebURL, policy, source)
	t.Cleanup(func() { nativeSnapshotGitLabSettle(t, client, project, run.ID) })

	deadline := time.Now().Add(25 * time.Minute)
	for run.Status != "success" && run.Status != "failed" && run.Status != "canceled" && run.Status != "skipped" {
		if time.Now().After(deadline) {
			t.Fatal("native snapshot pipeline deadline")
		}

		time.Sleep(3 * time.Second)

		run, _, err = client.Pipelines.GetPipeline(project, run.ID, gitlabapi.WithContext(t.Context()))
		if err != nil {
			t.Fatal(err)
		}
	}

	parentJobs, _, err := client.Jobs.ListPipelineJobs(project, run.ID, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(t.Context()))
	if err != nil {
		t.Fatal(err)
	}

	bridges, _, err := client.Jobs.ListPipelineBridges(project, run.ID, nil, gitlabapi.WithContext(t.Context()))
	if err != nil {
		t.Fatal(err)
	}

	jobs := parentJobs

	for _, bridge := range bridges {
		if bridge.DownstreamPipeline != nil {
			child, _, err := client.Jobs.ListPipelineJobs(project, bridge.DownstreamPipeline.ID, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(t.Context()))
			if err != nil {
				t.Fatal(err)
			}

			jobs = append(jobs, child...)
		}
	}

	trees := map[string]map[string][]byte{}

	for _, job := range jobs {
		t.Logf("native snapshot job: forge=gitlab pipeline=%d job=%d name=%s status=%s", job.Pipeline.ID, job.ID, job.Name, job.Status)

		if job.Status == "failed" {
			trace, _, _ := client.Jobs.GetTraceFile(project, job.ID, gitlabapi.WithContext(t.Context()))
			if trace != nil {
				body, _ := io.ReadAll(io.LimitReader(trace, 16384))
				t.Log(redactTargetSecrets(target, string(body)))
			}
		}

		if job.Name != "Snapshot: plan" && job.Name != "Snapshot: context" && job.Name != "Snapshot: collect" {
			continue
		}

		if job.Status != "success" {
			continue
		}

		archive, _, err := client.Jobs.GetJobArtifacts(project, job.ID, gitlabapi.WithContext(t.Context()))
		if err != nil {
			t.Fatal(err)
		}

		body, err := io.ReadAll(io.LimitReader(archive, 64<<20))
		if err != nil || len(body) >= 64<<20 {
			t.Fatal("native snapshot artifact incomplete")
		}

		saveSnapshotEvidence(t, fmt.Sprintf("gitlab-job-%d.zip", job.ID), body)
		tree, err := snapshotArtifactZIPTree(body, 32<<20)
		if err != nil {
			t.Fatal(err)
		}

		trees[job.Name] = tree
	}

	if run.Status != "success" {
		t.Fatalf("GitLab snapshot pipeline %d ended %s", run.ID, run.Status)
	}

	receipt, file, err := checkSnapshotArtifactEvidence(trees["Snapshot: plan"]["snapshot-plan.json"], trees["Snapshot: collect"]["container-images.json"], trees["Snapshot: context"]["snapshot-context/files/snapshot-dependencies.json"], source, imageRepository)
	if err != nil {
		t.Fatal(err)
	}

	options, err := registryRemoteOptions(target)
	if err != nil {
		t.Fatal(err)
	}

	snapshotRegistryCanary(t, t.Context(), receipt.Ref, receipt.Tag, target.CredentialUsername, target.Token, file.SHA256, source, options...)
	t.Logf("native snapshot accepted: forge=gitlab pipeline=%d candidate=%s policy=%s source=%s ref=%s tag=%s mode=%s sha256=%s", run.ID, engineSHA, policy, source, receipt.Ref, receipt.Tag, file.Mode, file.SHA256)
}

func nativeSnapshotGitLabSettle(t *testing.T, client *gitlabapi.Client, project string, id int64) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	defer cancel()

	run, _, err := client.Pipelines.GetPipeline(project, id, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Error("cannot observe exact snapshot pipeline during cleanup")

		return
	}

	if run.Status != "success" && run.Status != "failed" && run.Status != "canceled" && run.Status != "skipped" {
		if _, _, err := client.Pipelines.CancelPipelineBuild(project, id, gitlabapi.WithContext(ctx)); err != nil {
			t.Error("cannot cancel exact snapshot pipeline")
		}
	}
}
