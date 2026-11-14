// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v91/github"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestGitHubSnapshotArtifactFirst(t *testing.T) {
	RegisterLiveScenario(t)

	if os.Getenv(GitHubContextEnv) == "" {
		t.Skip("run inside a guarded hosted session")
	}

	id, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_ID"), 10, 64)
	login := os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_LOGIN")

	if err != nil || id <= 0 || login == "" {
		t.Fatal("explicit runtime-acknowledged organization ID/login required")
	}

	trial := OpenGitHubTrial(t, id)
	if trial.org != login {
		t.Fatal("context differs from runtime-acknowledged organization")
	}

	actor, _, err := trial.api.Users.Get(t.Context(), "")
	if err != nil {
		t.Fatal("cannot read authenticated actor")
	}

	org, response, err := trial.api.Organizations.Get(t.Context(), trial.org)
	if err != nil || org.GetID() != id || response == nil {
		t.Fatal("cannot read admitted organization")
	}

	scopes := map[string]bool{}
	for scope := range strings.SplitSeq(response.Header.Get("X-OAuth-Scopes"), ",") {
		scopes[strings.TrimSpace(scope)] = true
	}

	membership, _, err := trial.api.Organizations.GetOrgMembership(t.Context(), actor.GetLogin(), trial.org)
	if err != nil || !scopes["write:packages"] || !scopes["delete:packages"] || !scopes["workflow"] || !scopes["delete_repo"] || membership.GetRole() != "admin" {
		t.Fatal("package/workflow creation and exact cleanup authority not proven; no repositories created")
	}

	t.Log("package write/delete scopes and organization admin preflight passed before publication")

	// The engine this hosted session prepared: its runtime images carry the
	// forge-ci the container jobs run, and its canonical delivery serves the
	// host build legs. A copy of the tree without images cannot serve them.
	engine := trial.ReuseEngine(reporoot.Path(t))
	engineRepo, engineSHA := engine.Repository, engine.Commit
	images := map[string]string{}

	for _, name := range []string{"forge-ci-runtime-base", "forge-ci-runtime", "forge-ci-runtime-go-1.27"} {
		image, ok := engine.RuntimeImage(name)
		if !ok || !pinnedImage.MatchString(image) {
			t.Fatalf("every workflow admits images by digest only; the engine published no digest for %s", name)
		}

		images[name] = image
	}

	baseImage, fullImage, goImage := images["forge-ci-runtime-base"], images["forge-ci-runtime"], images["forge-ci-runtime-go-1.27"]

	t.Logf("snapshot engine under test: %s/%s commit=%s", trial.org, engineRepo, engineSHA)

	repo := minimalRepository(t, trial, "snapshot-artifact")

	details, _, err := trial.api.Repositories.Get(t.Context(), trial.org, repo)
	if err != nil {
		t.Fatal("cannot read acknowledged consumer")
	}

	repoID := details.GetID()
	// A unique new consumer name is also its image package. Never adopt or sweep
	// another package. Cleanup requires its ID and exact linked repository.
	_, existing, lookupErr := trial.api.Organizations.GetPackage(t.Context(), trial.org, "container", repo)
	if lookupErr == nil || existing == nil || existing.StatusCode != http.StatusNotFound {
		t.Fatal("snapshot package must be absent before this trial")
	}

	trial.RemoveSnapshotPackageAfter(repo, repoID)
	trial.SetVariable(repo, "FORGE_CI_REPOSITORY", trial.org+"/"+engineRepo)
	dir := trial.tempDir("snapshot-artifact")
	snapshotWriteFiles(t, dir, snapshotCanaryFiles(false))

	workflow := strings.NewReplacer("ENGINE_REPOSITORY", trial.org+"/"+engineRepo, "ENGINE_SHA", engineSHA, "BASE_IMAGE", baseImage, "FULL_IMAGE", fullImage, "GO_IMAGE", goImage).Replace(`name: Artifact first snapshot smoke
on:
  workflow_dispatch:
permissions:
  contents: read
  actions: read
  packages: write
jobs:
  canonical-cli:
    permissions:
      contents: read
      actions: read
    uses: ENGINE_REPOSITORY/.github/workflows/canonical-cli-bootstrap.yml@ENGINE_SHA
    with:
      engine-repository: ENGINE_REPOSITORY
      engine-commit: ENGINE_SHA
      runtime-image: GO_IMAGE
      receive-runtime-image: BASE_IMAGE
  snapshot:
    needs: canonical-cli
    uses: ENGINE_REPOSITORY/.github/workflows/snapshot-images.yml@ENGINE_SHA
    with:
      branch: feature/artifact-source
      forge-ci-binary-ref: ENGINE_SHA
      runtime-image: BASE_IMAGE
      runtime-image-full: FULL_IMAGE
      runtime-image-go: GO_IMAGE
      canonical-artifact-id: ${{ needs.canonical-cli.outputs.artifact-id }}
      canonical-artifact-name: ${{ needs.canonical-cli.outputs.artifact-name }}
      canonical-producer-run-id: ${{ needs.canonical-cli.outputs.producer-run-id }}
      canonical-producer-run-attempt: ${{ needs.canonical-cli.outputs.producer-run-attempt }}
      canonical-producer-engine-repository: ${{ needs.canonical-cli.outputs.engine-repository }}
      canonical-producer-engine-commit: ${{ needs.canonical-cli.outputs.engine-commit }}
      canonical-producer-result: ${{ needs.canonical-cli.result }}
`)
	minimalWrite(t, dir, ".github/workflows/snapshot-smoke.yml", workflow)
	trial.git(dir, "init", "--quiet", "--object-format=sha1", "--initial-branch=main")
	trial.git(dir, "add", "--all")
	trial.commit(dir, "test: trusted snapshot policy and distinct default source", "")
	policySHA := strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD"))
	trial.push(dir, trial.remote(repo), "HEAD:refs/heads/main")
	trial.git(dir, "checkout", "--quiet", "-b", snapshotArtifactBranch)
	snapshotWriteFiles(t, dir, snapshotCanaryFiles(true))
	trial.git(dir, "add", "--all")
	trial.commit(dir, "test: selected artifact source differs from policy branch", "")
	sourceSHA := strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD"))
	trial.push(dir, trial.remote(repo), "HEAD:refs/heads/"+snapshotArtifactBranch)
	t.Logf("snapshot source: consumer=%s/%s id=%d policy=%s selected=%s branch=%s", trial.org, repo, repoID, policySHA, sourceSHA, snapshotArtifactBranch)
	trial.waitWorkflows(repo, filepath.Join(dir, ".github", "workflows"))
	t.Cleanup(func() { settleSnapshotGitHubRun(t, trial, repo, policySHA) })
	trial.DispatchDevelopment(repo, "snapshot-smoke.yml", nil)
	run := trial.WaitRun(repo, "snapshot-smoke.yml", "workflow_dispatch", policySHA, 35*time.Minute)
	trial.ReportRun(repo, run)
	t.Logf("snapshot workflow: run=%d url=%s conclusion=%s", run.GetID(), run.GetHTMLURL(), run.GetConclusion())

	if run.GetConclusion() != "success" {
		t.Fatal("artifact-first snapshot workflow failed")
	}

	trees := snapshotGitHubArtifacts(t, trial, repo, run.GetID())
	imageRepository := "ghcr.io/" + strings.ToLower(trial.org+"/"+repo)

	receipt, file, err := checkSnapshotArtifactEvidence(trees["snapshot-images-inputs"]["snapshot-plan.json"], trees["snapshot-images"]["container-images.json"], trees["snapshot-images-context"]["files/snapshot-dependencies.json"], sourceSHA, imageRepository)
	if err != nil {
		t.Fatal(err)
	}

	canonical := trees["canonical"]
	if len(canonical["forge-ci-linux-amd64"]) == 0 || len(canonical["forge-ci-linux-arm64"]) == 0 {
		t.Fatal("missing canonical run engine artifact with both Linux architectures")
	}

	t.Logf("snapshot canonical run engine: amd64 sha256=%x engine=%s", sha256.Sum256(canonical["forge-ci-linux-amd64"]), engineSHA)
	snapshotRegistryCanary(t, t.Context(), receipt.Ref, receipt.Tag, actor.GetLogin(), trial.Token(), file.SHA256, sourceSHA)

	versions, _, err := trial.api.Organizations.PackageGetAllVersions(t.Context(), trial.org, "container", repo, &github.PackageListOptions{})
	if err != nil || len(versions) == 0 || len(versions) > 2 {
		t.Fatal("cannot inspect exact snapshot package versions")
	}

	for _, version := range versions {
		metadata, ok := version.GetMetadata()
		if !ok || metadata.Container == nil {
			t.Fatal("missing snapshot package container metadata")
		}

		for _, tag := range metadata.Container.Tags {
			if tag != receipt.Tag && tag != receipt.Tag+"-amd64" {
				t.Fatal("unexpected stable alias or release tag in snapshot package")
			}
		}

		t.Logf("snapshot package version: id=%d digest=%s tags=%v", version.GetID(), version.GetName(), metadata.Container.Tags)
	}

	trial.checkDevelopmentStableState(repo, policySHA)
	t.Logf("artifact-first snapshot accepted: source=%s policy=%s ref=%s tag=%s producer-file=%s mode=%s sha256=%s scans=true sbom=false", sourceSHA, policySHA, receipt.Ref, receipt.Tag, file.Path, file.Mode, file.SHA256)
}

func snapshotGitHubArtifacts(t *testing.T, trial *GitHubTrial, repo string, run int64) map[string]map[string][]byte {
	t.Helper()

	list, _, err := trial.api.Actions.ListWorkflowRunArtifacts(t.Context(), trial.org, repo, run, &github.ListOptions{PerPage: 100})
	if err != nil || list.GetTotalCount() != int64(len(list.Artifacts)) {
		t.Fatal("cannot completely list snapshot run artifacts")
	}

	out := map[string]map[string][]byte{}

	for _, artifact := range list.Artifacts {
		name := artifact.GetName()
		if strings.HasPrefix(name, "forge-ci-canonical-linux-") {
			name = "canonical"
		}

		if name != "snapshot-images-inputs" && name != "snapshot-images" && name != "snapshot-images-context" && name != "canonical" {
			continue
		}

		if out[name] != nil {
			t.Fatal("duplicate snapshot artifact")
		}

		location, _, err := trial.api.Actions.DownloadArtifact(t.Context(), trial.org, repo, artifact.GetID(), 0)
		if err != nil {
			t.Fatal("cannot request exact snapshot artifact")
		}

		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, location.String(), nil)
		if err != nil {
			t.Fatal("invalid artifact location")
		}

		response, err := (&http.Client{Timeout: 3 * time.Minute}).Do(request)
		if err != nil {
			t.Fatal("cannot download snapshot artifact")
		}

		body, err := io.ReadAll(io.LimitReader(response.Body, 128<<20))
		_ = response.Body.Close()

		if err != nil || response.StatusCode != http.StatusOK || len(body) >= 128<<20 {
			t.Fatal("snapshot artifact download incomplete or oversized")
		}

		tree, err := ZIPTree(body, 64<<20)
		if err != nil {
			t.Fatal(err)
		}

		out[name] = tree
		saveSnapshotEvidence(t, "github-"+name+".zip", body)
		t.Logf("snapshot artifact delivered: id=%d name=%s bytes=%d sha256=%x", artifact.GetID(), artifact.GetName(), len(body), sha256.Sum256(body))
	}

	return out
}

func settleSnapshotGitHubRun(t *testing.T, trial *GitHubTrial, repo, source string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	defer cancel()

	requested := map[int64]bool{}

	for {
		runs, _, err := trial.api.Actions.ListWorkflowRunsByFileName(ctx, trial.org, repo, "snapshot-smoke.yml", &github.ListWorkflowRunsOptions{HeadSHA: source, Event: "workflow_dispatch"})
		if err != nil {
			t.Error("cannot observe exact snapshot runs for cleanup")

			return
		}

		active := false

		for _, run := range runs.WorkflowRuns {
			if run.GetStatus() != "completed" {
				active = true

				if !requested[run.GetID()] {
					if _, err := trial.api.Actions.CancelWorkflowRunByID(ctx, trial.org, repo, run.GetID()); err != nil {
						t.Error("cannot cancel exact snapshot run")

						return
					}

					requested[run.GetID()] = true
				}
			}
		}

		if !active {
			t.Log("snapshot cleanup: owned runs terminal")

			return
		}

		select {
		case <-ctx.Done():
			t.Error("snapshot run cleanup deadline")

			return
		case <-time.After(2 * time.Second):
		}
	}
}
