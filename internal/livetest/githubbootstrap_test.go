// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v91/github"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// This is a bootstrap-only working-tree snapshot, not EnsureEngine or a release
// trial: no signing authority, release assets, runtime images or production pushes.
func TestGitHubBootstrap_SourceAndStableSelection(t *testing.T) {
	RegisterLiveScenario(t)
	if os.Getenv(GitHubContextEnv) == "" {
		t.Skip("run inside the documented Forge Lab github run session")
	}

	id, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_ID"), 10, 64)
	if err != nil || id <= 0 {
		t.Fatal("bootstrap live test requires an explicit positive organization ID admitted by Forge Lab")
	}

	trial := OpenGitHubTrial(t, id)

	organization, response, err := trial.api.Organizations.Get(t.Context(), trial.org)
	if err != nil || organization.GetID() != id || response == nil {
		t.Fatalf("read admitted bootstrap organization before creation: %v", err)
	}

	if err := bootstrapClassicScopes(response.Header.Get("X-OAuth-Scopes")); err != nil {
		t.Fatal(err)
	}

	repo := minimalRepository(t, trial, "bootstrap")
	trial.SetVariable(repo, "FORGE_CI_REPOSITORY", "diggsweden/forge-ci")
	dir := trial.tempDir("bootstrap")
	root := reporoot.Path(t)

	var manifest strings.Builder

	for _, name := range []string{"scripts/bootstrap/install-forge-ci.sh", "scripts/bootstrap/install-common.sh", "scripts/bootstrap/install-cosign.sh", ".github/workflows/resolve-forge-ci-bootstrap.yml"} {
		minimalCopy(t, filepath.Join(root, name), filepath.Join(dir, name))

		body, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Fatal(readErr)
		}

		sum := fmt.Sprintf("%x", sha256.Sum256(body))
		fmt.Fprintf(&manifest, "%s  %s\n", sum, name)
		t.Logf("candidate file sha256=%s path=%s", sum, name)
	}

	minimalWrite(t, dir, "bootstrap-files.sha256", manifest.String())
	minimalWrite(t, dir, ".github/workflows/bootstrap-smoke.yml", strings.ReplaceAll(githubBootstrapWorkflow, "SOURCE_COMMIT", bootstrapSourceCommit))
	trial.git(dir, "init", "--quiet", "--object-format=sha1", "--initial-branch=main")
	trial.git(dir, "add", "--all")
	trial.commit(dir, "test: snapshot candidate bootstrap and stable resolver", "")
	snapshot := strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD^{tree}"))
	t.Logf("candidate scratch snapshot: %s/%s commit=%s tree=%s", trial.org, repo, snapshot, tree)
	trial.push(dir, trial.remote(repo), "HEAD:refs/heads/main")
	trial.waitWorkflows(repo, filepath.Join(dir, ".github", "workflows"))
	// Register before dispatch: cleanup settles only this new repository's runs,
	// including an uncertain dispatch response, before its ID-bound deletion.
	t.Cleanup(func() { settleBootstrapRuns(t, trial, repo, snapshot) })
	trial.DispatchDevelopment(repo, "bootstrap-smoke.yml")
	run := trial.WaitRun(repo, "bootstrap-smoke.yml", "workflow_dispatch", snapshot, 22*time.Minute)
	t.Logf("bootstrap workflow: run=%d url=%s", run.GetID(), run.GetHTMLURL())
	trial.ReportRun(repo, run)

	if run.GetConclusion() != "success" {
		t.Fatalf("bootstrap workflow ended %s", run.GetConclusion())
	}

	jobs, _, err := trial.api.Actions.ListWorkflowJobs(t.Context(), trial.org, repo, run.GetID(), &github.ListWorkflowJobsOptions{Filter: "latest", PerPage: 100})
	if err != nil || jobs.GetTotalCount() != 3 || len(jobs.Jobs) != 3 {
		t.Fatalf("expected exactly three bootstrap jobs: %v", err)
	}

	verified := false

	for _, job := range jobs.Jobs {
		t.Logf("bootstrap job: id=%d name=%s conclusion=%s", job.GetID(), job.GetName(), job.GetConclusion())

		if job.GetConclusion() != "success" {
			t.Fatal("every selected bootstrap job must succeed")
		}

		if job.GetName() != "Verify bootstrap source and stable selection" {
			continue
		}

		location, _, logErr := trial.api.Actions.GetWorkflowJobLogs(t.Context(), trial.org, repo, job.GetID(), 0)
		if logErr != nil {
			t.Fatalf("read exact bootstrap job log: %v", logErr)
		}

		proof := failureOf(t.Context(), location.String(), 100)

		stable, binary, proofErr := bootstrapResult(proof, snapshot, tree)
		if proofErr != nil {
			t.Fatal(proofErr)
		}

		runtimeProof := "BOOTSTRAP_RUNTIME stable=ghcr.io/diggsweden/forge-ci-runtime-base:" + stable + " explicit=ghcr.io/diggsweden/forge-ci-runtime-base:sha-e916432"
		if !strings.Contains(proof, runtimeProof) {
			t.Fatal("missing actual resolver runtime-output proof")
		}

		t.Logf("bootstrap verified: source=%s stable=%s binary-sha256=%s candidate=%s tree=%s", bootstrapSourceCommit, stable, binary, snapshot, tree)
		t.Log(runtimeProof)

		verified = true
	}

	if !verified {
		t.Fatal("missing successful bootstrap evidence job")
	}

	trial.checkDevelopmentStableState(repo, snapshot)

	artifacts, _, err := trial.api.Actions.ListWorkflowRunArtifacts(t.Context(), trial.org, repo, run.GetID(), &github.ListOptions{PerPage: 1})
	if err != nil || artifacts.GetTotalCount() != 0 {
		t.Fatalf("bootstrap-only smoke must create no artifacts: %v", err)
	}
}

func settleBootstrapRuns(t *testing.T, trial *GitHubTrial, repo, snapshot string, selection ...string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	defer cancel()

	requested := make(map[int64]bool)
	workflow, event := "bootstrap-smoke.yml", "workflow_dispatch"
	if len(selection) == 2 {
		workflow, event = selection[0], selection[1]
	}

	for {
		runs, _, err := trial.api.Actions.ListWorkflowRunsByFileName(ctx, trial.org, repo, workflow, &github.ListWorkflowRunsOptions{HeadSHA: snapshot, Event: event})
		if err != nil {
			t.Errorf("bootstrap cleanup cannot observe owned runs: %v", err)

			return
		}

		active := false

		for _, run := range runs.WorkflowRuns {
			if run.GetStatus() == "completed" {
				continue
			}

			active = true

			if !requested[run.GetID()] {
				if _, err := trial.api.Actions.CancelWorkflowRunByID(ctx, trial.org, repo, run.GetID()); err != nil {
					t.Errorf("cancel owned bootstrap run %d: %v", run.GetID(), err)

					return
				}

				requested[run.GetID()] = true
			}
		}

		if !active {
			t.Log("bootstrap cleanup: all observed owned runs terminal")

			return
		}

		select {
		case <-ctx.Done():
			t.Error("bootstrap cleanup timed out waiting for owned runs")

			return
		case <-time.After(2 * time.Second):
		}
	}
}
