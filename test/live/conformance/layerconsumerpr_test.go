// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// The consumer the pull request is opened in: one Go module, whose source is
// beside the fixtures, and a canary file the pull request changes.
const (
	consumerPRCanary = "consumer-pr-canary.txt"

	consumerPRGoMod = "module example.invalid/consumer-pr-canary\n\ngo 1.27.4\n"

	consumerPRBuildJob = "Build consumer pull request"
	consumerPRLintJob  = "Lint consumer pull request"

	// consumerPRLintImageEnv names the nanolinter image of the Go flavour the
	// lint finding is judged in, pinned by digest: the image a Go consumer's
	// CI_JOB_IMAGE names.
	consumerPRLintImageEnv = "FORGE_CI_LIVE_PR_LINT_JOB_IMAGE"
)

// consumerPRLintTests are the pull-request owner's lint cases: a finding the
// administrator's enforce-lint decides, and a gate that cannot run, which no
// setting waives.
var consumerPRLintTests = []struct {
	name, source string
	// unavailable runs the gate where it has no nanolinter.
	unavailable bool
}{
	{name: "lint-findings", source: consumerPRLintFinding},
	{name: "lint-tool-unavailable", unavailable: true},
}

// consumerPRCase is what one case of the pull-request owner varies: the
// workflow the pull request runs and its facts, what main holds in place of
// or beside the module, what the pull request changes beside the canary, the
// job that change is judged in, the jobs that succeed whatever it does,
// whether it must stop the job after it, and what its log must hold.
type consumerPRCase struct {
	fixture     string
	facts       []string
	base        map[string]string
	changes     map[string]string
	gate        string
	independent []string
	blocked     bool
	log         func(t *testing.T, log string)
}

// applicationTestPRCase is the build workflow's case: clean without a source,
// else the adverse Go test the administrator's enforce-application-tests
// decides.
func applicationTestPRCase(t *testing.T, adverseTest string) consumerPRCase {
	t.Helper()

	enforced := livetest.AdminInputs(t, nil)["enforce-application-tests"].(bool)
	pr := consumerPRCase{fixture: layerConsumerPRFixture, gate: consumerPRBuildJob, independent: []string{"Verify consumer release isolation"}}

	if adverseTest != "" {
		pr.changes = map[string]string{"main_test.go": adverseTest}
		pr.blocked = enforced || adverseTest == consumerPRCompileError
		pr.log = func(t *testing.T, log string) {
			t.Helper()
			requireAdverseTestLog(t, log, adverseTest, enforced)
		}
	}

	return pr
}

// lintPRCase is the lint workflow's case. A finding runs in the nanolinter
// image the run names and is the administrator's enforce-lint to decide; the
// control runs in the build fixture's Go image, which holds no nanolinter,
// and fails under either setting.
func lintPRCase(t *testing.T, source string, unavailable bool) consumerPRCase {
	t.Helper()

	enforced := livetest.AdminInputs(t, nil)["enforce-lint"].(bool)
	image := consumerPRLinterlessImage

	if !unavailable {
		image = os.Getenv(consumerPRLintImageEnv)
		if image == "" {
			// A full run without the image has not judged the lint finding,
			// and must not read as if it had.
			livetest.NotCoveredf(t, "set %s to a digest-pinned nanolinter image of the Go flavour; the lint finding is not judged without one", consumerPRLintImageEnv)
		}

		if !container.ValidDigestReference(image) {
			t.Fatalf("%s must be digest-pinned", consumerPRLintImageEnv)
		}
	}

	pr := consumerPRCase{
		fixture: layerConsumerPRLintFixture, facts: []string{"RENDER_LINT_JOB_IMAGE", image},
		base: consumerPRLintBase, gate: consumerPRLintJob, blocked: enforced || unavailable,
		log: func(t *testing.T, log string) {
			t.Helper()

			for _, problem := range consumerPRLintLogProblems(log, unavailable, enforced) {
				t.Error(problem)
			}
		},
	}

	if source != "" {
		pr.changes = map[string]string{"lint.go": source}
	}

	return pr
}

// LAYER-3: a consumer's pull-request workflow builds, tests and passes the
// isolation gate with the layer's actions, and publishes nothing.
//
// The clean case's three jobs assert for themselves: the layer's Go build produces a binary
// stamped with the version the caller gave; the checkout is the pull
// request's head, in this repository, for a pull_request event from the head
// branch into main, and holds the change the pull request makes; and
// check-l3-isolation accepts this repository's own release example, rendered
// as a consumer of the served layer.
//
// The host then asks the forge: the pull request's run is the three jobs,
// each successful on a first attempt on the runner label; the pull request is
// still open and unmerged at the same commits; no branch moved; and the run
// left no tag, release, artifact, deploy key or secret, and no package on the
// account. A pull request is unreviewed code, so "it published nothing" is
// the half of this scenario that matters most.
//
// The two adverse cases change only the PR's Go test source. A real assertion
// failure must reach the binary verification and downstream identity job under
// report-only policy, and block both under enforcing policy. A compiler error
// blocks under either policy. Layer.Render supplies the current administrator
// input from the pinned action contract, not from application source. These
// are PR non-publication checks, not proof of a release publisher being gated.
//
// The two lint cases run the lint workflow instead, with the layer's lint
// gate in the build's place and the same downstream identity job. A go vet
// finding in the PR's source must reach that job under report-only
// enforce-lint and block it under enforcement, counted as one lint finding
// and no vulnerability. The control runs the gate in an image without
// nanolinter: it cannot run, says so, and blocks under either setting. The
// finding needs a nanolinter image the run names (consumerPRLintImageEnv).
func TestForgejoLayer_ConsumerPullRequest(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			format := pairedObjectFormat(visibility)
			t.Run(format, func(t *testing.T) {
				t.Run("clean", func(t *testing.T) { runLayerConsumerPR(t, visibility.Private, format, applicationTestPRCase(t, "")) })

				for _, adverse := range consumerPRAdverseTests {
					t.Run(adverse.name, func(t *testing.T) {
						runLayerConsumerPR(t, visibility.Private, format, applicationTestPRCase(t, adverse.source))
					})
				}

				for _, lint := range consumerPRLintTests {
					t.Run(lint.name, func(t *testing.T) {
						runLayerConsumerPR(t, visibility.Private, format, lintPRCase(t, lint.source, lint.unavailable))
					})
				}
			})
		})
	}
}

func runLayerConsumerPR(t *testing.T, private bool, objectFormat string, pr consumerPRCase) {
	t.Helper()

	const (
		workflow = "layer-consumer-pr"
		head     = "layer-pull-request"
	)

	scenario := newLayerScenario(t, "layer-consumer-pr", private, objectFormat)
	target, repo := scenario.target, scenario.repo

	example, err := os.ReadFile(filepath.Join(reporoot.Path(t), "examples/go-cli/forgejo/release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	release, err := scenario.layer.RenderReleaseExample(string(example))
	if err != nil {
		t.Fatal(err)
	}

	// One commit: neither workflow starts on a push to a branch, and the pull
	// request should show the one change it makes.
	files := map[string]string{
		"go.mod":                         consumerPRGoMod,
		"main.go":                        consumerPRMain,
		"main_test.go":                   consumerPRMainTest,
		consumerPRCanary:                 "consumer-pr-base\n",
		".forgejo/workflows/release.yml": release,
		".forgejo/workflows/" + workflow + ".yml": scenario.render(t, pr.fixture, pr.facts...),
	}
	maps.Copy(files, pr.base)

	main := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
		Branch: layerMain, Message: "livetest: the consumer", Files: files,
	})

	changes := map[string]string{consumerPRCanary: "consumer-pr-feature\n"}
	maps.Copy(changes, pr.changes)

	feature := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
		Branch: layerMain, NewBranch: head, Message: "livetest: the change under review",
		Files: changes,
	})

	packages := livetest.OwnerPackages(t, target)

	opened := livetest.OpenPullRequest(t, target, repo, head, layerMain, "livetest: consumer pull request")
	requirePullRequest(t, opened, head, feature, main)

	run := livetest.WaitForLayerRun(t, target, repo, workflow, "pull_request")

	// A job the failed gate withholds is one no runner took. The status the
	// forge gives it is its own: it listed the unstarted test job as blocked
	// in lab run 568 and as skipped in run 572 (2026-11-10), so the empty
	// want stands for never started and is judged by the task, not the label.
	wantRun, wantGate, wantDownstream := jobSuccess, jobSuccess, jobSuccess
	if pr.blocked {
		wantRun, wantGate, wantDownstream = jobFailure, jobFailure, ""
	}

	jobs := scenario.requireStatus(t, run, wantRun)
	requireRunIDs(t, target, repo, run.ID)
	requireJobsOnRunner(t, jobs, run.ID, scenario.label)

	wantJobs := map[string]string{pr.gate: wantGate, "Test consumer pull request": wantDownstream}
	for _, name := range pr.independent {
		wantJobs[name] = jobSuccess
	}

	wantStatus := maps.Clone(wantJobs)

	if len(jobs) != len(wantJobs) {
		t.Fatalf("got %d jobs, want %d", len(jobs), len(wantJobs))
	}

	for _, job := range jobs {
		started := job.Started()

		switch want, ok := wantJobs[job.Name]; {
		case !ok:
			t.Errorf("job %q (%s) is not one the pull request's workflow names", job.Name, job.Status)
		case want == "" && started:
			t.Errorf("a runner took job %q (%s) after the gate failed, want it never started", job.Name, job.Status)
		case want != "" && job.Status != want:
			t.Errorf("job %q is %s, want %s", job.Name, job.Status, want)
		}

		delete(wantJobs, job.Name)

		if started && !slices.Equal(job.RunsOn, []string{scenario.label}) {
			t.Errorf("job %q ran on %v, want [%s]", job.Name, job.RunsOn, scenario.label)
		}

		if pr.log != nil && job.Name == pr.gate {
			pr.log(t, livetest.JobLog(t, target, repo, job))
		}
	}

	if len(wantJobs) != 0 {
		t.Errorf("missing jobs: %v", wantJobs)
	}

	requirePullRequest(t, livetest.ReadPullRequest(t, target, repo, opened.Number), head, feature, main)

	scenario.requireBranches(t, map[string]string{layerMain: main, head: feature})
	scenario.requireNone(t, "/tags", "/releases", "/keys", "/actions/secrets")
	scenario.requireSamePackages(t, packages)

	if artifacts := livetest.RunArtifacts(t, target, repo, run.ID); len(artifacts) != 0 {
		t.Errorf("the pull request's run left artifacts: %+v", artifacts)
	}

	// Read last, after everything above: a job the forge still lists as
	// blocked is one it could yet hand to a runner.
	for _, job := range livetest.RunJobs(t, target, repo, run.ID) {
		if want, ok := wantStatus[job.Name]; ok && want == "" && job.Started() {
			t.Errorf("a runner took job %q (%s) after the run ended, want it never started", job.Name, job.Status)
		}
	}
}

// requireAdverseTestLog holds a build job's log to the Go failure the adverse
// source causes and, under report-only policy, to the engine's own
// classification of it. The release owner's build is held to the same.
func requireAdverseTestLog(t *testing.T, log, adverseTest string, enforced bool) {
	t.Helper()

	marker := "controlled application assertion"
	if adverseTest == consumerPRCompileError {
		marker = "undefined: enforcementCompileError"
	}

	if !strings.Contains(log, marker) {
		t.Errorf("build log lacks the actual Go failure %q", marker)
	}

	if !enforced {
		receipt := "application-tests result=findings count=1 enforcement=false source=flag"
		if adverseTest == consumerPRCompileError {
			receipt = "application-tests result=not-waivable enforcement=false source=flag"
		}

		if !strings.Contains(log, receipt) {
			t.Errorf("build log lacks the engine classification %q", receipt)
		}
	}
}

// requirePullRequest holds a pull request to open and unmerged, from head at
// its commit into main at its commit.
func requirePullRequest(t *testing.T, pull livetest.PullRequest, head, headCommit, mainCommit string) {
	t.Helper()

	if pull.Number <= 0 || pull.State != "open" || pull.Merged {
		t.Fatalf("the pull request is #%d, %s, merged %t; want an open one", pull.Number, pull.State, pull.Merged)
	}

	if pull.Head.Ref != head || pull.Head.SHA != headCommit || pull.Base.Ref != layerMain || pull.Base.SHA != mainCommit {
		t.Errorf("the pull request is %+v into %+v, want %s@%s into %s@%s", pull.Head, pull.Base, head, headCommit, layerMain, mainCommit)
	}
}
