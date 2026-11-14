// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/google/go-github/v91/github"
	"gopkg.in/yaml.v3"
)

// Exercise GitHub's actual needs semantics, which a CLI black-box suite and
// a YAML string check cannot prove: the gates run whatever the repository
// variables say, and a failed gate or release publishes nothing. Keep the
// production self-release DAG/conditions; replace expensive or privileged job
// effects with inert witnesses. This proves scheduling, not binary builds or
// asset signing.
func TestGitHubSelfRelease_BinaryScheduling(t *testing.T) {
	t.Parallel()

	RegisterLiveScenario(t)

	if os.Getenv(GitHubContextEnv) == "" {
		t.Skip("run test-live-github-self-release inside a guarded Forge Lab session")
	}

	id, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_ID"), 10, 64)
	if err != nil || id <= 0 {
		t.Fatal("an explicit positive GitHub organization ID is required")
	}

	// The gates always run. The variable that once waived them is set to
	// false in every case: it must no longer be read.
	for _, test := range []struct {
		name           string
		failed         string // the job that fails, or none
		releaseResult  string
		binaryResult   string
		workflowResult string
	}{
		{"gates-pass", "", "success", "success", "success"},
		{"failed-gate", "blackbox", "skipped", "skipped", "failure"},
		{"failed-release", "release", "failure", "skipped", "failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			trial := OpenGitHubTrial(t, id)
			repo := minimalRepository(t, trial, "self-release-"+test.name)
			trial.SetVariable(repo, "SELF_RELEASE_INTEGRATION_GATES", "false")
			dir := trial.tempDir("self-release-gating")
			workflow, release := selfReleaseSchedulingFixtures(t, test.failed)
			minimalWrite(t, dir, ".github/workflows/self-release-gating.yml", workflow)
			minimalWrite(t, dir, ".github/workflows/release-witness.yml", release)
			minimalWrite(t, dir, ".github/workflows/binary-witness.yml", selfReleaseBinaryWitness)
			trial.git(dir, "init", "--quiet", "--object-format=sha1", "--initial-branch=main")
			trial.git(dir, "add", "--all")
			trial.commit(dir, "test: witness candidate self-release scheduling", "")
			commit := strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD"))

			t.Cleanup(func() { settleOwnedRuns(t, trial, repo, commit, "self-release-gating.yml", "push") })
			trial.push(dir, trial.remote(repo), "HEAD:refs/heads/main")
			run := trial.WaitRun(repo, "self-release-gating.yml", "push", commit, 15*time.Minute)
			t.Logf("self-release scheduling witness: %s", run.GetHTMLURL())
			trial.ReportRun(repo, run)

			if run.GetConclusion() != test.workflowResult {
				t.Fatalf("workflow ended %s, want %s", run.GetConclusion(), test.workflowResult)
			}

			selfReleaseSchedulingResults(t, trial, repo, run.GetID(), test.failed, test.releaseResult, test.binaryResult)
		})
	}
}

func selfReleaseSchedulingFixtures(t *testing.T, failed string) (string, string) {
	t.Helper()

	body := reporoot.ReadFile(t, ".github/workflows/self-release.yml")

	var source struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Needs any    `yaml:"needs"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &source); err != nil {
		t.Fatal(err)
	}

	jobs := map[string]any{}

	for name, sourceJob := range source.Jobs {
		job := map[string]any{"name": name}
		if sourceJob.If != "" {
			job["if"] = sourceJob.If
		}

		if sourceJob.Needs != nil {
			job["needs"] = sourceJob.Needs
		}

		switch name {
		case "release", "binary":
			job["uses"] = "./.github/workflows/" + name + "-witness.yml"
		case "resolve-branch", "checksums", "blackbox", "evidence", "wait-for-images":
			job["runs-on"] = "ubuntu-26.04"

			job["steps"] = []map[string]string{{"run": "true"}}
			if name == failed {
				job["steps"] = []map[string]string{{"run": "false"}}
			}
		default:
			t.Fatalf("review new self-release job %q before projecting its scheduling", name)
		}

		jobs[name] = job
	}

	workflow := map[string]any{
		"name":        "Self release scheduling witness",
		"on":          map[string]any{"push": map[string]any{"branches": []string{"main"}}},
		"permissions": map[string]string{"contents": "read"},
		"jobs":        jobs,
	}

	encoded, err := yaml.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}

	exitCode := 0
	if failed == "release" {
		exitCode = 1
	}

	return string(encoded), fmt.Sprintf("name: Release witness\non:\n  workflow_call:\njobs:\n  release:\n    name: Release witness\n    runs-on: ubuntu-26.04\n    steps:\n      - run: exit %d\n", exitCode)
}

const selfReleaseBinaryWitness = "name: Binary witness\non:\n  workflow_call:\njobs:\n  binary:\n    name: Publish binary witness\n    runs-on: ubuntu-26.04\n    steps:\n      - run: echo binary-publication-witness-executed\n"

func selfReleaseSchedulingResults(t *testing.T, trial *GitHubTrial, repo string, runID int64, failed, releaseResult, binaryResult string) {
	t.Helper()

	page, _, err := trial.api.Actions.ListWorkflowJobs(t.Context(), trial.org, repo, runID, &github.ListWorkflowJobsOptions{Filter: "latest", PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}

	if page.GetTotalCount() != len(page.Jobs) {
		t.Fatal("incomplete scheduling-witness job inventory")
	}

	results := map[string]string{}
	for _, job := range page.Jobs {
		results[job.GetName()] = job.GetConclusion()
	}

	// Both gates ran: neither is skipped, whatever the variable says.
	for _, name := range []string{"blackbox", "evidence"} {
		want := "success"
		if name == failed {
			want = "failure"
		}

		if results[name] != want {
			t.Errorf("%s ended %q, want %s", name, results[name], want)
		}
	}

	// A called workflow's job is listed under its caller; a skipped caller
	// is listed by its own name.
	for caller, want := range map[string]string{"release": releaseResult, "binary": binaryResult} {
		name := caller
		if want != "skipped" {
			name = map[string]string{"release": "release / Release witness", "binary": "binary / Publish binary witness"}[caller]
		}

		if results[name] != want {
			t.Fatalf("%s ended %q, want %s; jobs=%v", caller, results[name], want, results)
		}
	}
}
