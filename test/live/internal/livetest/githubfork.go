// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v91/github"

	"github.com/diggsweden/forge-ci/internal/retry"
)

// The steps of a build that offers private dependencies which decide a fork
// pull request: the refusal, and the checkout it must come before. They are
// the names .github/workflows/build-{npm,maven,gradle-app}.yml give them.
const (
	forkRefusalStep  = "Refuse private dependency access from fork pull requests"
	forkCheckoutStep = "Checkout repository"
)

// ForkConsumer forks the consumer repository into the same organization as
// <repo>-fork, waits for its main, and deletes the fork by the ID its
// creation returned when the test ends. A pull request from it is a fork pull
// request: its head repository is not the consumer.
func (trial *GitHubTrial) ForkConsumer(repo string) string {
	trial.tb.Helper()

	ctx := trial.tb.Context()
	name := repo + "-fork"

	// GitHub answers 202 while it copies the repository; the body names it.
	fork, response, err := trial.api.Repositories.CreateFork(ctx, trial.org, repo, &github.RepositoryCreateForkOptions{Organization: trial.org, Name: name, DefaultBranchOnly: true})
	if fork == nil || fork.GetID() <= 0 || !fork.GetFork() || !strings.EqualFold(fork.GetOwner().GetLogin(), trial.org) {
		trial.tb.Fatalf("livetest: fork %s as %s: no fork in %s was acknowledged: %v", repo, name, trial.org, err)
	}

	// Whatever name GitHub gave it, the fork is this test's to delete.
	made, id := fork.GetName(), fork.GetID()

	trial.tb.Logf("disposable fork: %s/%s id=%d", trial.org, made, id)
	trial.tb.Cleanup(func() { trial.removeConsumer(made, id) })

	if made != name {
		trial.tb.Fatalf("livetest: fork %s was made as %s, want %s", repo, made, name)
	}

	if err != nil && (response == nil || response.StatusCode != http.StatusAccepted) {
		trial.tb.Fatalf("livetest: fork %s as %s: %v", repo, name, err)
	}

	wait, cancel := context.WithTimeout(ctx, githubIndexTimeout)
	defer cancel()

	if err := retry.Run(wait, nil, 60, 5*time.Second, func() error {
		_, _, err := trial.api.Repositories.GetBranch(wait, trial.org, name, defaultBranch, 0)

		return err
	}, retry.WithBackoff(retry.Constant)); err != nil {
		trial.tb.Fatalf("livetest: the fork %s has no %s: %v", name, defaultBranch, err)
	}

	return name
}

// OpenForkPullRequest opens branch of fork against main of repo and returns
// its number.
func (trial *GitHubTrial) OpenForkPullRequest(repo, fork, branch string) int {
	trial.tb.Helper()

	// Both repositories are the organization's, so the owner in head names
	// neither: GitHub takes the head repository by its full name, and refused
	// the bare name as an invalid head (hosted trial, 2026-11-10).
	pull, _, err := trial.api.PullRequests.Create(trial.tb.Context(), trial.org, repo, github.CreatePullRequest{
		Title: new("Hosted trial: fork pull request"), Head: trial.org + ":" + branch, HeadRepo: new(trial.org + "/" + fork), Base: defaultBranch,
		Body: new("Forge Lab hosted consumer trial. Disposable."),
	})
	if err != nil {
		trial.tb.Fatalf("livetest: open the pull request from %s:%s: %v", fork, branch, err)
	}

	if head := pull.GetHead().GetRepo().GetName(); head != fork {
		trial.tb.Fatalf("livetest: the pull request's head repository is %q, want the fork %s", head, fork)
	}

	return pull.GetNumber()
}

// CheckForkRefusal wants the run of a fork pull request refused where the
// engine promises: the build's refusal step failed and its checkout never
// ran, so no step that holds the run token as a dependency credential did.
func (trial *GitHubTrial) CheckForkRefusal(repo string, run *github.WorkflowRun) {
	trial.tb.Helper()

	jobs, _, err := trial.api.Actions.ListWorkflowJobs(trial.tb.Context(), trial.org, repo, run.GetID(), &github.ListWorkflowJobsOptions{PerPage: 100})
	if err != nil {
		trial.tb.Fatalf("livetest: jobs of run %d: %v", run.GetID(), err)
	}

	if err := forkRefusalGap(run.GetConclusion(), jobs.Jobs); err != nil {
		trial.tb.Fatalf("livetest: the fork pull request's run %d: %v", run.GetID(), err)
	}
}

// forkRefusalGap judges the jobs of a fork pull request's run: the run
// failed, exactly one job ran the refusal step and it failed there, and in
// that job it is the workflow's first step and the checkout and every later
// step of the workflow were skipped. The runner's own opening and closing
// steps are not the workflow's and may run.
func forkRefusalGap(conclusion string, jobs []*github.WorkflowJob) error {
	if conclusion != runFailure {
		return fmt.Errorf("ended %s, want failure: %w", conclusion, errReleaseEvidence)
	}

	var refused int

	for _, job := range jobs {
		ran, err := forkRefusalJobGap(job)
		if err != nil {
			return err
		}

		if ran {
			refused++
		}
	}

	if refused != 1 {
		return fmt.Errorf("%d jobs ran the step %q, want 1: %w", refused, forkRefusalStep, errReleaseEvidence)
	}

	return nil
}

// forkRefusalJobGap reports whether job ran the refusal step, and refuses a
// job in which that step did not fail first, before a checkout, with every
// later step skipped.
func forkRefusalJobGap(job *github.WorkflowJob) (bool, error) {
	refusal, checkout := -1, -1

	for index, step := range job.Steps {
		switch step.GetName() {
		case forkRefusalStep:
			refusal = index
		case forkCheckoutStep:
			checkout = index
		}
	}

	if refusal < 0 {
		return false, nil
	}

	if got := job.Steps[refusal].GetConclusion(); got != runFailure {
		return true, fmt.Errorf("job %q: the refusal step ended %s, want failure: %w", job.GetName(), got, errReleaseEvidence)
	}

	for _, step := range job.Steps[:refusal] {
		if name := step.GetName(); name != "Set up job" && name != "Initialize containers" {
			return true, fmt.Errorf("job %q: step %q precedes the refusal, want the refusal first: %w", job.GetName(), name, errReleaseEvidence)
		}
	}

	if checkout < refusal {
		return true, fmt.Errorf("job %q: no checkout step follows the refusal: %w", job.GetName(), errReleaseEvidence)
	}

	for _, step := range job.Steps[refusal+1:] {
		name := step.GetName()
		if name == "Complete job" || name == "Stop containers" || strings.HasPrefix(name, "Post ") {
			continue
		}

		if got := step.GetConclusion(); got != verdictSkipped {
			return true, fmt.Errorf("job %q: step %q ended %s after the refusal, want skipped: %w", job.GetName(), name, got, errReleaseEvidence)
		}
	}

	return true, nil
}
