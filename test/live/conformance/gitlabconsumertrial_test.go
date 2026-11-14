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
	"slices"
	"strings"
	"testing"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// The consumer's real MR gate runs on its committed source. An inert misplaced
// release key first proves the job refuses it and GitLab blocks the MR. Scoping
// that same canary to the protected signer environment then permits the actual
// checks, on the same MR commit. No signing credential is used by this trial.
func TestGitLabLayer_ConsumerTrial(t *testing.T) {
	livetest.RegisterAdmittedScenario(t)

	forEachConsumerCheckout(t, provider.ForgeGitLab, "just test-live-consumer gitlab <checkout> pull-request job-archive=<prepared-job-image.tar>", func(t *testing.T, root string) {
		t.Helper()

		for _, visibility := range livetest.ConsumerVisibilities(t) {
			t.Run(visibility.Name, func(t *testing.T) {
				runGitLabConsumerTrial(t, root, visibility.Private)
			})
		}
	}, livetest.TrialPullRequest)
}

func runGitLabConsumerTrial(t *testing.T, root string, private bool) {
	t.Helper()

	if livetest.DemoName() != "" {
		t.Fatal("this trial plants a failing canary; a GitLab demo is `just demo gitlab <name>`")
	}

	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "the consumer trial needs a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)

	consumer := livetest.OpenConsumer(t, target, root)
	if gate := os.Getenv("FORGE_CI_LIVE_GITLAB_GATE_SOURCE"); gate != "" {
		consumer = consumer.WithGitLabGateFromSource(t, gate)
	}

	livetest.Admitted(t)

	engineRepo := livetest.NewPublicScratchRepo(t, target, "gitlab-consumer-layer", livetest.ReadLayerAnonymously)
	layer := livetest.PushLayer(t, target, engineRepo)
	consumer = consumer.WithDraftLayer(t, layer)
	// A further job image of the gate is often the consumer image's archive.
	pushed := livetest.JobImages{}
	image := pushed.Push(t, target, os.Getenv("FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE"), func() string { return engineRepo })
	repo := livetest.NewBareScratchRepo(t, target, "gitlab-consumer-trial", consumer.ObjectFormat, private)

	images := livetest.GitLabGateImages{Job: image, ExtraJobs: livetest.PushGitLabGateExtraJobImages(t, target, consumer, pushed, func(name string) string {
		return livetest.NewPublicScratchRepo(t, target, "gitlab-consumer-job-image-"+strings.ToLower(name), livetest.ReadJobImageAnonymously)
	})}
	if archive := os.Getenv("FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE"); archive != "" {
		images.Lint = livetest.PushJobImage(t, target, livetest.NewPublicScratchRepo(t, target, "gitlab-consumer-lint-image", livetest.ReadJobImageAnonymously), archive)
	}

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	project := livetest.RepoSlug(target, repo)

	ctx, cancel := context.WithTimeout(livetest.Context(t), 60*time.Minute)
	defer cancel()

	installGitLabConsumerPolicy(t, client, target, project, consumer, layer, images, private)

	main := consumer.PushGitLab(t, repo, layer, images)
	requireObjectFormats(t, consumer.ObjectFormat, main, layer.Commit)
	head := consumer.PushChange(t, repo, trialBranch, main)
	t.Logf("consumer %s at %s (%s); layer %s; job image %s", root, consumer.Commit, consumer.ObjectFormat, layer.Commit, image)

	const (
		variable = "RELEASE_COSIGN_PRIVATE_KEY"
		canary   = "inert-signer-scope-canary"
	)
	if _, _, writeErr := client.ProjectVariables.CreateVariable(project, &gitlabapi.CreateProjectVariableOptions{
		Key: gitlabapi.Ptr(variable), Value: gitlabapi.Ptr(canary), EnvironmentScope: new("*"),
	}, gitlabapi.WithContext(ctx)); writeErr != nil {
		t.Fatal(writeErr)
	}

	livetest.RetainGitLabJobLogsAfter(t, target, repo, map[string]string{variable: canary})

	waitGitLabPush(t, ctx, client, project, "main", main)
	waitGitLabPush(t, ctx, client, project, trialBranch, head)

	mr, err := livetest.CreateGitLabMergeRequest(ctx, client, project, &gitlabapi.CreateMergeRequestOptions{
		SourceBranch: new(trialBranch), TargetBranch: new("main"), Title: new("test: GitLab consumer trial"),
	})
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
		EnvironmentScope: new("release-signing"), Protected: new(true),
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
		livetest.FailOnRunConclusionf(t, "the consumer's own MR gate ended %s", passed.Status)
	}
	// Success must retain the same bounded trace evidence before scratch
	// cleanup; summaries alone lose Rust test counts and gate diagnostics.
	passedTrace := gitlabTrialTrace(t, ctx, client, project, passed.ID, "check", "Check: build and test", "Check: lint")
	t.Log(strings.ReplaceAll(passedTrace, target.Token, "[REDACTED]"))

	settings, _, err := client.Projects.GetProject(project, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if !settings.OnlyAllowMergeIfPipelineSucceeds || settings.AllowMergeOnSkippedPipeline {
		t.Fatal("the project must require successful, non-skipped pipelines")
	}
}

// The harness owns this independent policy project. The consumer's CI files
// retain no enforcement settings and cannot replace the pinned gate includes.
func installGitLabConsumerPolicy(t *testing.T, client *gitlabapi.Client, target livetest.Target,
	project string, consumer livetest.Consumer, layer livetest.Layer, images livetest.GitLabGateImages, private bool,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	body := consumer.GitLabAdminPolicy(t, layer, images)
	repo := livetest.NewBareScratchRepo(t, target, "consumer-admin-policy", "sha1", private)
	policy := gitlabPolicyCommit(t, ctx, client, livetest.RepoSlug(target, repo), livetest.GitLabAdminGateFile, body, gitlabapi.FileCreate)
	useGitLabAdminPolicy(ctx, t, client, project, livetest.GitLabAdminGateFile+"@"+livetest.RepoSlug(target, repo)+":"+policy.ID)
}

func installGitLabDevelopmentPolicy(t *testing.T, client *gitlabapi.Client, target livetest.Target,
	project string, consumer livetest.Consumer, layer livetest.Layer, images livetest.GitLabGateImages, private bool,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	pipeline, gate := consumer.GitLabDevelopmentPolicy(t, layer, images)
	repo := livetest.NewBareScratchRepo(t, target, "development-admin-policy", "sha1", private)
	policyProject := livetest.RepoSlug(target, repo)
	gitlabPolicyCommit(t, ctx, client, policyProject, livetest.GitLabAdminGateFile, gate, gitlabapi.FileCreate)
	policy := gitlabPolicyCommit(t, ctx, client, policyProject, livetest.GitLabAdminPipelineFile, pipeline, gitlabapi.FileCreate)
	useGitLabAdminPolicy(ctx, t, client, project, livetest.GitLabAdminPipelineFile+"@"+policyProject+":"+policy.ID)
}

// waitGitLabPush waits for GitLab to finish processing a push the harness
// made before it opens a merge request: one opened earlier is refreshed by
// the late push and gets a second merge-request pipeline.
func waitGitLabPush(t *testing.T, ctx context.Context, client *gitlabapi.Client, project, branch, sha string) {
	t.Helper()

	if err := livetest.WaitGitLabPushProcessed(ctx, client, project, branch, sha); err != nil {
		t.Fatal(err)
	}
}

func waitGitLabMRPipeline(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, mr int64, sha string, id int64) *gitlabapi.Pipeline {
	t.Helper()

	discoveryDeadline := time.Now().Add(2 * time.Minute)
	started := time.Now()
	lastStatus := ""
	nextProgress := started

	for {
		if id == 0 {
			id = findGitLabMRPipeline(t, ctx, client, project, mr)
			if id == 0 && time.Now().After(discoveryDeadline) {
				t.Fatal("GitLab created no merge-request pipeline within two minutes; inspect the consumer workflow rules")
			}
		}

		if id != 0 {
			pipeline, err := livetest.PollGitLabPipeline(ctx, client, project, id)
			if err != nil {
				t.Fatal(err)
			}

			if pipeline.SHA != sha || pipeline.Source != "merge_request_event" {
				t.Fatalf("pipeline %d: source=%q sha=%s; want merge_request_event at %s", id, pipeline.Source, pipeline.SHA, sha)
			}

			if pipeline.Status != lastStatus || !time.Now().Before(nextProgress) {
				t.Logf("GitLab MR !%d pipeline %d: %s after %s (%s)", mr, id, pipeline.Status, time.Since(started).Round(time.Second), pipeline.WebURL)
				lastStatus, nextProgress = pipeline.Status, time.Now().Add(2*time.Minute)
			}

			switch pipeline.Status {
			case "success", "failed", "canceled", "skipped", "manual":
				// Before any judgement: cleanup deletes the project and
				// with it GitLab's own account of the failure.
				livetest.LogGitLabPipelineFailure(t, ctx, client, project, pipeline)

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
		detail, readErr := livetest.PollGitLabPipeline(ctx, client, project, candidate.ID)
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
		PerPage: 100,
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	// Producers' traces first and failed jobs' last, each under its job's
	// name: the end of the text, which gitlabTraceEnd shows, is then a failed
	// job's, whatever order GitLab lists the jobs in.
	var produced, failed strings.Builder

	var failures []livetest.JobFailure

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

		trace := gitlabFinishedTrace(t, ctx, client, project, job.ID)
		fmt.Fprintf(into, "\n=== job %s (%s) ===\n", job.Name, job.Status)
		into.WriteString(trace)

		if job.Status == "failed" && !job.AllowFailure {
			failures = append(failures, livetest.ClassifyGitLabJob(job.Name, job.FailureReason, trace))
		}
	}

	// Every failure judgement reads the jobs here first: a pipeline the lab
	// failed ends as infrastructure, with its traces kept, before any caller
	// can call it the product's.
	if livetest.OnlyInfrastructure(failures) {
		logGitLabTrace(t, failed.String())
		livetest.StopOnInfrastructure(t, failures)
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
