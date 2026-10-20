// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"os"
	"strings"
	"testing"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

// The consumer writes inert provenance after declaring its release files, before
// the normal build handoff validates dist. Reject before any signer can use it.
func TestGitLabRelease_RejectsPreseededProvenance(t *testing.T) {
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		livetest.NotCoveredf(t, "requires a reviewed consumer that preseeds dist/slsa-provenance.intoto.json after release files declare")
	}
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabReleaseScenario(t, root, gitlabPreseededProvenanceRelease, visibility.Private)
		})
	}
}

func requireGitLabPreseededProvenanceRejection(t *testing.T, ctx context.Context, client *gitlabapi.Client, project, publication string, run *gitlabapi.Pipeline) {
	t.Helper()
	if run.Status != "failed" {
		t.Fatalf("preseeded provenance pipeline ended %s, want failed", run.Status)
	}
	bridges, _, err := client.Jobs.ListPipelineBridges(project, run.ID, nil, gitlabapi.WithContext(ctx))
	if err != nil || len(bridges) != 1 || bridges[0].DownstreamPipeline == nil {
		t.Fatalf("release child pipeline was not created: %v", err)
	}
	child := bridges[0].DownstreamPipeline.ID
	jobs, _, err := client.Jobs.ListPipelineJobs(project, child, &gitlabapi.ListJobsOptions{ListOptions: gitlabapi.ListOptions{PerPage: 100}}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	var rejected, signerBlocked bool
	for _, job := range jobs {
		switch job.Name {
		case "Build: release files":
			rejected = job.Status == "failed"
		case "Sign: files and images":
			signerBlocked = job.Status == "skipped" || job.Status == "created"
		}
		if strings.HasPrefix(job.Name, "Publish:") && (job.Status == "success" || job.Status == "running") {
			t.Errorf("publication job %s ended %s after preseeded provenance", job.Name, job.Status)
		}
	}
	if !rejected || !signerBlocked {
		logGitLabTrace(t, gitlabTrialTrace(t, ctx, client, project, child))
		t.Fatalf("want rejected unsigned handoff and blocked signer; build failed=%t, signer blocked=%t", rejected, signerBlocked)
	}
	const reason = "unsigned dist must not hold dist/slsa-provenance.intoto.json: signing writes it"
	trace := gitlabTrialTrace(t, ctx, client, project, child, "Build: release files")
	if !strings.Contains(trace, reason) {
		logGitLabTrace(t, trace)
		t.Fatalf("unsigned handoff did not reject preseeded provenance with %q", reason)
	}
	for _, slug := range []string{project, publication} {
		releases, _, err := client.Releases.ListReleases(slug, nil, gitlabapi.WithContext(ctx))
		if err != nil || len(releases) != 0 {
			t.Fatalf("expected no releases in %s: count=%d, error=%v", slug, len(releases), err)
		}
	}
	t.Logf("parent %d child %d: unsigned handoff rejected %q, signing/publication blocked and Releases API empty", run.ID, child, reason)
}
