// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// The consumer's real MR gate runs on its committed source. An inert misplaced
// release key first proves the job refuses it and GitLab blocks the MR. Scoping
// that same canary to the protected signer environment then permits the actual
// checks, on the same MR commit. No signing credential is used by this trial.
func TestGitLabLayer_ConsumerTrial(t *testing.T) {
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("use just test-live-gitlab-consumer <checkout> <prepared-job-image.tar>")
	}
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabConsumerTrial(t, root, visibility.Private)
		})
	}
}

func runGitLabConsumerTrial(t *testing.T, root string, private bool) {
	t.Helper()
	if livetest.DemoName() != "" {
		t.Fatal("this trial plants a failing canary; a GitLab demo is `just demo gitlab <name> <checkout>`")
	}

	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "the consumer trial needs a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	consumer := livetest.OpenConsumer(t, target, root)
	engineRepo := livetest.NewScratchRepo(t, target, "gitlab-consumer-layer")
	layer := livetest.PushLayer(t, target, engineRepo)
	image := livetest.PushJobImage(t, target, engineRepo, os.Getenv("FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE"))
	repo := livetest.NewBareScratchRepo(t, target, "gitlab-consumer-trial", consumer.ObjectFormat, private)
	images := livetest.GitLabGateImages{Job: image}
	if archive := os.Getenv("FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE"); archive != "" {
		images.Lint = livetest.PushJobImage(t, target, livetest.NewScratchRepo(t, target, "gitlab-consumer-lint-image"), archive)
	}
	main := consumer.PushGitLab(t, repo, layer, images)
	requireObjectFormats(t, consumer.ObjectFormat, main, layer.Commit)
	head := consumer.PushChange(t, repo, trialBranch, main)
	t.Logf("consumer %s at %s (%s); layer %s; job image %s", root, consumer.Commit, consumer.ObjectFormat, layer.Commit, image)

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	project := livetest.RepoSlug(target, repo)
	ctx, cancel := context.WithTimeout(livetest.Context(t), 60*time.Minute)
	defer cancel()

	const variable = "RELEASE_COSIGN_PRIVATE_KEY"
	const canary = "inert-signer-scope-canary"
	if _, _, writeErr := client.ProjectVariables.CreateVariable(project, &gitlabapi.CreateProjectVariableOptions{
		Key: gitlabapi.Ptr(variable), Value: gitlabapi.Ptr(canary), EnvironmentScope: gitlabapi.Ptr("*"),
	}, gitlabapi.WithContext(ctx)); writeErr != nil {
		t.Fatal(writeErr)
	}

	// GitLab takes a pushed branch in after git has returned, and refuses a
	// merge request opened before then: "source_branch does not exist".
	if waitErr := retry.Run(ctx, nil, 30, 2*time.Second, func() error {
		branch, _, readErr := client.Branches.GetBranch(project, trialBranch, gitlabapi.WithContext(ctx))
		if readErr != nil || branch.Commit == nil || branch.Commit.ID != head {
			return fmt.Errorf("branch %s is not at %s yet: %w", trialBranch, head, errs.ErrDependencyUnavailable)
		}

		return nil
	}, retry.WithBackoff(retry.Constant)); waitErr != nil {
		t.Fatal(waitErr)
	}

	mr, _, err := client.MergeRequests.CreateMergeRequest(project, &gitlabapi.CreateMergeRequestOptions{
		SourceBranch: gitlabapi.Ptr(trialBranch), TargetBranch: gitlabapi.Ptr("main"), Title: gitlabapi.Ptr("test: GitLab consumer trial"),
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	failed := waitGitLabMRPipeline(t, ctx, client, project, mr.IID, head, 0)
	if failed.Status != "failed" {
		t.Fatalf("misplaced signing material produced pipeline %d status %q", failed.ID, failed.Status)
	}
	trace := gitlabTrialTrace(t, ctx, client, project, failed.ID)
	if strings.Contains(trace, canary) {
		t.Fatal("the refusal exposed the misplaced credential's value")
	}
	if !strings.Contains(trace, variable+" must not be available") {
		// The job failed for another reason; its trace is the only place that says which.
		t.Log(trace)
		t.Log(gitlabTraceEnd(trace))
		t.Fatal("the check job failed without naming the misplaced credential")
	}
	blocked, _, err := client.MergeRequests.GetMergeRequest(project, mr.IID, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if blocked.DetailedMergeStatus != "ci_must_pass" {
		t.Fatalf("failed checks did not block the MR: status %q", blocked.DetailedMergeStatus)
	}

	if _, _, writeErr := client.ProjectVariables.UpdateVariable(project, variable, &gitlabapi.UpdateProjectVariableOptions{
		EnvironmentScope: gitlabapi.Ptr("release-signing"), Protected: gitlabapi.Ptr(true),
	}, gitlabapi.WithContext(ctx)); writeErr != nil {
		t.Fatal(writeErr)
	}

	rerun, _, err := client.MergeRequests.CreateMergeRequestPipeline(project, mr.IID, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	passed := waitGitLabMRPipeline(t, ctx, client, project, mr.IID, head, rerun.ID)
	t.Logf("GitLab MR !%d at %s: refusal pipeline %d blocked merge; scoped-variable pipeline %d ended %s",
		mr.IID, head, failed.ID, passed.ID, passed.Status)
	if passed.Status != "success" {
		logGitLabTrace(t, gitlabTrialTrace(t, ctx, client, project, passed.ID))
		t.Fatalf("the consumer's own MR gate ended %s", passed.Status)
	}

	settings, _, err := client.Projects.GetProject(project, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if !settings.OnlyAllowMergeIfPipelineSucceeds || settings.AllowMergeOnSkippedPipeline {
		t.Fatal("the project must require successful, non-skipped pipelines")
	}
}

func waitGitLabMRPipeline(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, mr int64, sha string, id int64) *gitlabapi.Pipeline {
	t.Helper()
	discoveryDeadline := time.Now().Add(2 * time.Minute)
	for {
		if id == 0 {
			id = findGitLabMRPipeline(t, ctx, client, project, mr)
			if id == 0 && time.Now().After(discoveryDeadline) {
				t.Fatal("GitLab created no merge-request pipeline within two minutes; inspect the consumer workflow rules")
			}
		}
		if id != 0 {
			pipeline, _, err := client.Pipelines.GetPipeline(project, id, gitlabapi.WithContext(ctx))
			if err != nil {
				t.Fatal(err)
			}
			if pipeline.SHA != sha || pipeline.Source != "merge_request_event" {
				t.Fatalf("pipeline %d: source=%q sha=%s; want merge_request_event at %s", id, pipeline.Source, pipeline.SHA, sha)
			}
			switch pipeline.Status {
			case "success", "failed", "canceled", "skipped", "manual":
				return pipeline
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("the GitLab consumer pipeline exceeded its bounded wait")
		case <-time.After(3 * time.Second):
		}
	}
}

func findGitLabMRPipeline(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, mr int64) int64 {
	t.Helper()

	pipelines, _, err := client.MergeRequests.ListMergeRequestPipelines(project, mr, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	var id int64

	for _, candidate := range pipelines {
		detail, _, readErr := client.Pipelines.GetPipeline(project, candidate.ID, gitlabapi.WithContext(ctx))
		if readErr != nil {
			t.Fatal(readErr)
		}

		if detail.Source != "merge_request_event" {
			continue
		}

		if id != 0 {
			t.Fatal("the initial MR trial unexpectedly created multiple MR pipelines")
		}

		id = detail.ID
	}

	return id
}

func gitlabTrialTrace(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, pipeline int64, producers ...string) string {
	t.Helper()
	jobs, _, err := client.Jobs.ListPipelineJobs(project, pipeline, &gitlabapi.ListJobsOptions{
		ListOptions: gitlabapi.ListOptions{PerPage: 100},
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	// Producers' traces first and failed jobs' last, each under its job's
	// name: the end of the text, which gitlabTraceEnd shows, is then a failed
	// job's, whatever order GitLab lists the jobs in.
	var produced, failed strings.Builder
	for _, job := range jobs {
		t.Logf("job %s: %s", job.Name, job.Status)
		if job.ArtifactsFile.Size > 0 {
			t.Logf("job %s: native artifact archive %d bytes", job.Name, job.ArtifactsFile.Size)
		}
		into := &failed
		if job.Status != "failed" {
			if job.Status != "success" || !slices.Contains(producers, job.Name) {
				continue
			}
			into = &produced
		}
		fmt.Fprintf(into, "\n=== job %s (%s) ===\n", job.Name, job.Status)
		into.WriteString(gitlabFinishedTrace(t, ctx, client, project, job.ID))
	}

	return produced.String() + failed.String()
}

// gitlabFinishedTrace reads a finished job's trace once GitLab holds all of
// it: the runner streams a trace in chunks, and a job reads as finished before
// its last chunk, which carries the error, is stored. GitLab's own closing
// line ends a whole trace; after a minute the trace is shown as it is.
func gitlabFinishedTrace(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, job int64) string {
	t.Helper()

	deadline := time.Now().Add(time.Minute)

	for {
		trace, _, err := client.Jobs.GetTraceFile(project, job, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}

		body, err := io.ReadAll(io.LimitReader(trace, 8<<20))
		if err != nil {
			t.Fatal(err)
		}

		if gitlabTraceFinished(string(body)) || time.Now().After(deadline) {
			return string(body)
		}

		select {
		case <-ctx.Done():
			return string(body)
		case <-time.After(2 * time.Second):
		}
	}
}

// gitlabTraceFinished reports whether a trace carries the runner's closing
// line.
func gitlabTraceFinished(trace string) bool {
	return strings.Contains(trace, "Job succeeded") || strings.Contains(trace, "ERROR: Job failed")
}

// logGitLabTrace prints the failed jobs' whole trace and then, last, its end
// in plain text: a reader starts at the bottom, and the cause is nearly
// always there.
func logGitLabTrace(t *testing.T, trace string) {
	t.Helper()
	t.Log(trace)
	t.Log(gitlabTraceEnd(trace))
}

// gitlabTraceNoise is what GitLab adds to every trace line and a reader does
// not need: the timestamp and stream prefix, and terminal escapes.
var gitlabTraceNoise = regexp.MustCompile(`^\S+Z \d\d[OE][+ ]?|\x1b\[[0-9;]*[A-Za-z]`)

// gitlabTraceEnd is the last lines of a trace without that noise and without
// GitLab's section markers.
func gitlabTraceEnd(trace string) string {
	const keep = 40

	var lines []string

	for line := range strings.SplitSeq(strings.TrimSpace(trace), "\n") {
		line = strings.TrimSpace(gitlabTraceNoise.ReplaceAllString(strings.TrimSpace(line), ""))
		if line == "" || strings.HasPrefix(line, "section_start:") || strings.HasPrefix(line, "section_end:") {
			continue
		}

		lines = append(lines, line)
	}

	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}

	return "the end of the failed job's trace:\n" + strings.Join(lines, "\n")
}
