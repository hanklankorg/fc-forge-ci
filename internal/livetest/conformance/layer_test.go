// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

// LAYER-*: the Forgejo delivery layer, run by a real Forgejo runner.
//
// Every other scenario here drives the CLI. These drive the layer around it:
// the actions and workflows under .forgejo/, reached the way a consumer
// reaches them, by a reference to a repository at a pinned commit. What only a
// real instance can show is how the runner fetches that reference, expands a
// called workflow, resolves `vars` and secrets, hands outputs from one job to
// the next, and what the run leaves on the forge.
//
// The layer under test is this checkout's: PushLayer serves HEAD's tree from a
// branch of the scratch repository, and the fixture pins that commit. The
// binary a job runs is the one that commit's checksum names, which PushLayer
// publishes to the scratch owner's generic packages and the job downloads as
// the layer delivers it; it is not the build the rest of this tier hands over
// by path.
//
// This file is what the scenarios share. Each scenario has its own file and
// its fixture under testdata/layer.

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

const (
	// layerRunnerVariable is the repository variable every workflow of the
	// layer schedules by.
	layerRunnerVariable = "CI_RUNNER_LABEL"

	// containerRunnerVariable is the repository variable a job that builds
	// images with Buildah schedules by instead, when it is set.
	containerRunnerVariable = "CI_RUNNER_LABEL_CONTAINER"

	// layerMain is the consumer branch a fixture workflow is committed to.
	layerMain = "main"

	jobSuccess   = "success"
	jobFailure   = "failure"
	runCancelled = "cancelled"
)

// layerScenario is a consumer scratch repository using a separately served
// delivery layer and scheduling on the contract's runner.
type layerScenario struct {
	target livetest.Target
	repo   string
	layer  livetest.Layer
	label  string
}

// newLayerScenario skips where no Forgejo runner exists, and otherwise
// returns a fresh repository with the layer pushed and the runner variable
// set. No workflow is committed yet, so nothing has run.
func newLayerScenario(t *testing.T, name string, private bool, objectFormat string) layerScenario {
	t.Helper()

	target := livetest.Accept(t, provider.ForgeForgejo)
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner) {
		t.Skip("no Forgejo runner in this environment, so the layer cannot run")
	}

	scenario := layerScenario{target: target, label: livetest.RunnerLabel(t, target)}
	scenario.layer = livetest.PushLayer(t, target, livetest.NewScratchRepo(t, target, livetest.LayerScenario(name)))
	scenario.repo = livetest.NewBareScratchRepo(t, target, name, objectFormat, private)
	seed := livetest.CommitFiles(t, target, scenario.repo, livetest.FileCommit{
		Branch: layerMain, Message: "test: initialize the layer consumer",
		Files: map[string]string{"README.md": "Disposable layer consumer.\n"},
	})
	requireObjectFormats(t, objectFormat, seed, scenario.layer.Commit)
	livetest.EnableActions(t, target, scenario.repo)

	t.Logf("layer under test: commit %s, binary sha256 %s, runner label %q",
		scenario.layer.Commit, scenario.layer.BinarySHA256, scenario.label)

	livetest.SetRepoVariable(t, target, scenario.repo, layerRunnerVariable, runnerLabelsJSON(t, scenario.label))

	return scenario
}

func runnerLabelsJSON(t *testing.T, label string) string {
	t.Helper()

	encoded, err := json.Marshal([]string{label})
	if err != nil {
		t.Fatal(err)
	}

	return string(encoded)
}

// privilegedRunner is the contract's runner that permits user namespaces, or
// skips: a scenario that builds images with Buildah cannot run on the
// ordinary runner, which may deny them.
func privilegedRunner(t *testing.T, target livetest.Target) string {
	t.Helper()

	label := livetest.PrivilegedRunnerLabel(t, target)
	if label == "" {
		t.Skip("the contract names no runner that permits user namespaces, which rootless Buildah needs; " +
			"start the lab's runners with the full-release acknowledgement and mint the contract again")
	}

	return label
}

// render fills a fixture for this scenario's repository.
func (s layerScenario) render(t *testing.T, fixture string, facts ...string) string {
	t.Helper()

	facts = append(facts, "RENDER_ACCEPTANCE_REPOSITORY", livetest.RepoSlug(s.target, s.repo))

	rendered, err := s.layer.Render(fixture, facts...)
	if err != nil {
		t.Fatal(err)
	}

	return rendered
}

// pairedObjectFormat gives a runner scenario one hash format per visibility:
// public with SHA-1, private with SHA-256. The two are independent, so the
// other two combinations would cost a runner job each and settle nothing.
// TestRepository_ObjectFormatRoundTrip runs all four without a runner, which
// is where a failure here is told apart.
func pairedObjectFormat(visibility livetest.ConsumerVisibility) string {
	if visibility.Private {
		return "sha256"
	}

	return "sha1"
}

// The helper checkout stays SHA-1; SHA-256 consumer cases therefore exercise
// cross-repository mixed-format delivery, rather than Git format conversion.
func requireObjectFormats(t *testing.T, objectFormat, consumerCommit, engineCommit string) {
	t.Helper()

	want := 40
	if objectFormat == "sha256" {
		want = 64
	}

	if len(consumerCommit) != want || len(engineCommit) != 40 {
		t.Fatalf("object format mismatch: consumer=%s commit=%s engine=%s", objectFormat, consumerCommit, engineCommit)
	}

	t.Logf("object formats verified: consumer=%s, engine=sha1, mixed=%t", objectFormat, objectFormat != "sha1")
}

// requireStatus retains the run's job evidence before checking its verdict.
func (s layerScenario) requireStatus(t *testing.T, run livetest.WorkflowRun, want string, secrets ...map[string]string) []livetest.RunJob {
	t.Helper()

	jobs := livetest.RunJobs(t, s.target, s.repo, run.ID)
	s.logJobs(t, jobs, secrets...)
	if run.Status == want {
		return jobs
	}

	t.Fatalf("run %d of %s ended %q, want %s", run.ID, run.WorkflowID, run.Status, want)

	return nil
}

// logJobs retains bounded redacted runner traces before scratch cleanup,
// including successful builds whose test counts would otherwise disappear.
// Only unsuccessful jobs print a tail to the operator log.
func (s layerScenario) logJobs(t *testing.T, jobs []livetest.RunJob, secrets ...map[string]string) {
	t.Helper()

	for _, job := range jobs {
		// The status first: the run script redacts by shape, and a job named
		// for a token followed by a colon and a word has that shape.
		t.Logf("%-9s job %q", job.Status, job.Name)

		if job.Status == "skipped" || len(job.RunsOn) == 0 {
			continue
		}

		if log := livetest.RetainJobLog(t, s.target, s.repo, job, secrets...); log != "" {
			if job.Status == jobSuccess {
				continue
			}
			t.Logf("job %q log (tail):\n%s", job.Name, log)
		} else {
			t.Logf("job %q: the forge served no log", job.Name)
		}
	}
}

// requireBranches holds the repository to exactly the named branches, each at
// the named commit: a run that pushed, or left a branch behind, differs.
func (s layerScenario) requireBranches(t *testing.T, want map[string]string) {
	t.Helper()

	got := livetest.Branches(t, s.target, s.repo)
	if len(got) != len(want) {
		t.Errorf("the repository has branches %v, want exactly %v", got, want)

		return
	}

	for name, commit := range want {
		if got[name] != commit {
			t.Errorf("branch %s is at %q, want %s", name, got[name], commit)
		}
	}
}

// requireNone holds the repository to none of each kind, by API route.
func (s layerScenario) requireNone(t *testing.T, routes ...string) {
	t.Helper()

	for _, route := range routes {
		if count := livetest.RepoCount(t, s.target, s.repo, route); count != 0 {
			t.Errorf("the repository holds %d of %s, want none", count, route)
		}
	}
}

// requireSamePackages holds the owner's packages to what they were before.
func (s layerScenario) requireSamePackages(t *testing.T, before []livetest.OwnerPackage) {
	t.Helper()

	if after := livetest.OwnerPackages(t, s.target); !slices.Equal(before, after) {
		t.Errorf("the owner's packages changed during the run:\nbefore %+v\nafter  %+v", before, after)
	}
}

// requireJobsOnRunner holds each job to a first attempt of this run under a
// distinct id, and returns the names of the jobs that ran on the runner label
// and of the ones that did not: a job that calls a workflow has no runner.
func requireJobsOnRunner(t *testing.T, jobs []livetest.RunJob, runID int64, label string) ([]string, []string) {
	t.Helper()

	var onRunner, wrappers []string

	seen := make(map[int64]bool, len(jobs))

	for _, job := range jobs {
		if job.ID <= 0 || seen[job.ID] || job.RunID != runID || job.Attempt != 1 {
			t.Errorf("job %q is not a distinct first attempt of run %d: %+v", job.Name, runID, job)
		}

		seen[job.ID] = true

		switch {
		case slices.Equal(job.RunsOn, []string{label}):
			onRunner = append(onRunner, job.Name)
		case len(job.RunsOn) == 0:
			wrappers = append(wrappers, job.Name)
		default:
			t.Errorf("job %q runs on %v, want the runner label %q or no runner", job.Name, job.RunsOn, label)
		}
	}

	slices.Sort(onRunner)
	slices.Sort(wrappers)

	return onRunner, wrappers
}
