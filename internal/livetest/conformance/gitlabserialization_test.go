// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Run the production parent against a tiny, pinned child with a blocking manual
// job. No signing or build toolchain is needed to observe GitLab's lock lifetime.
// Only the child include is substituted; rules, resource group, mirror strategy,
// variable forwarding and interruption policy come from the actual component.
func TestGitLabRelease_ResourceGroupSpansTheChild(t *testing.T) {
	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "release serialization requires a GitLab runner")
	}
	target := livetest.Accept(t, provider.ForgeGitLab)
	policyRepo := livetest.NewScratchRepo(t, target, "serialization-policy")
	repo := livetest.NewScratchRepo(t, target, "serialization-consumer")
	project, policyProject := livetest.RepoSlug(target, repo), livetest.RepoSlug(target, policyRepo)
	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(livetest.Context(t), 6*time.Minute)
	defer cancel()

	source := gitlabPolicyCommit(t, ctx, client, project, "serialization.txt", "two requests share one source commit\n", gitlabapi.FileCreate)
	child := gitlabPolicyCommit(t, ctx, client, policyProject, "child.yml", gitlabSerializationChild, gitlabapi.FileCreate)
	policy := gitlabPolicyCommit(t, ctx, client, policyProject, "policy.yml", gitlabSerializationPolicy(t, policyProject, child.ID), gitlabapi.FileCreate)
	if _, _, err = client.Projects.EditProject(project, &gitlabapi.EditProjectOptions{
		CIConfigPath:                           gitlabapi.Ptr("policy.yml@" + policyProject + ":" + policy.ID),
		CIPipelineVariablesMinimumOverrideRole: gitlabapi.Ptr(gitlabapi.CIPipelineVariablesMinimumOverrideRoleValue("no_one_allowed")),
		CIPushRepositoryForJobTokenAllowed:     gitlabapi.Ptr(false),
	}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = client.ProtectedTags.ProtectRepositoryTags(project, &gitlabapi.ProtectRepositoryTagsOptions{
		Name: gitlabapi.Ptr("release-request/*"), CreateAccessLevel: gitlabapi.Ptr(gitlabapi.MaintainerPermissions),
	}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
	const firstTag, secondTag = "release-request/v0.0.1", "release-request/v0.0.2"
	create := func(tag string) {
		t.Helper()
		if _, _, createErr := client.Tags.CreateTag(project, &gitlabapi.CreateTagOptions{TagName: gitlabapi.Ptr(tag), Ref: gitlabapi.Ptr(source.ID)}, gitlabapi.WithContext(ctx)); createErr != nil {
			t.Fatal(createErr)
		}
	}
	create(firstTag)
	first := waitGitLabReleaseBridge(t, ctx, client, project, firstTag, false)
	firstGate := waitGitLabChildGate(t, ctx, client, project, first.DownstreamPipeline.ID)
	create(secondTag)
	second := waitGitLabReleaseBridge(t, ctx, client, project, secondTag, true)
	if first.Pipeline.ID == second.Pipeline.ID {
		t.Fatal("two request tags did not create distinct parents")
	}

	// A queued bridge, with no child, while the first child waits at a later
	// job proves the lock was not released when the trigger merely started it.
	if _, _, err = client.Jobs.PlayJob(project, firstGate, nil, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
	waitGitLabParentSuccess(t, ctx, client, project, first.Pipeline.ID)
	second = waitGitLabReleaseBridge(t, ctx, client, project, secondTag, false)
	secondGate := waitGitLabChildGate(t, ctx, client, project, second.DownstreamPipeline.ID)
	if _, _, err = client.Jobs.PlayJob(project, secondGate, nil, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
	waitGitLabParentSuccess(t, ctx, client, project, second.Pipeline.ID)
	t.Logf("release lock held through child %d; parent %d waited without a child until parent %d succeeded, then child %d completed",
		first.DownstreamPipeline.ID, second.Pipeline.ID, first.Pipeline.ID, second.DownstreamPipeline.ID)
}

func gitlabSerializationPolicy(t *testing.T, project, sha string) string {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(reporoot.ReadFile(t, "templates/release.yml")))
	var spec any
	if err := decoder.Decode(&spec); err != nil {
		t.Fatal(err)
	}
	var pipeline map[string]any
	if err := decoder.Decode(&pipeline); err != nil {
		t.Fatal(err)
	}
	parent, ok := pipeline["release"].(map[string]any)
	if !ok {
		t.Fatal("release component has no parent job")
	}
	trigger, ok := parent["trigger"].(map[string]any)
	if !ok {
		t.Fatal("release parent has no child trigger")
	}
	trigger["include"] = []any{map[string]any{"project": project, "ref": sha, "file": "/child.yml"}}
	body, err := yaml.Marshal(pipeline)
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func waitGitLabReleaseBridge(t *testing.T, ctx context.Context, client *gitlabapi.Client, project, tag string, queued bool) *gitlabapi.Bridge {
	t.Helper()
	var result *gitlabapi.Bridge
	awaitGitLabObservation(t, ctx, func() bool {
		runs, _, err := client.Pipelines.ListProjectPipelines(project, &gitlabapi.ListProjectPipelinesOptions{Ref: gitlabapi.Ptr(tag)}, gitlabapi.WithContext(ctx))
		if err != nil || len(runs) > 1 {
			t.Fatalf("observe request pipeline: count=%d err=%v", len(runs), err)
		}
		if len(runs) == 0 {
			return false
		}
		bridges, _, err := client.Jobs.ListPipelineBridges(project, runs[0].ID, nil, gitlabapi.WithContext(ctx))
		if err != nil || len(bridges) > 1 {
			t.Fatalf("observe release bridge: count=%d err=%v", len(bridges), err)
		}
		if len(bridges) == 0 {
			return false
		}
		result = bridges[0]
		if queued {
			if result.DownstreamPipeline != nil {
				t.Fatal("second release created its child while the first still held its gate")
			}

			return result.Status == "waiting_for_resource"
		}

		return result.DownstreamPipeline != nil
	})

	return result
}

func waitGitLabChildGate(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, pipeline int64) int64 {
	t.Helper()
	var gate int64
	awaitGitLabObservation(t, ctx, func() bool {
		jobs, _, err := client.Jobs.ListPipelineJobs(project, pipeline, nil, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		ready := false
		for _, job := range jobs {
			if job.Name == "prepared" && job.Status == "success" {
				ready = true
			}
			if job.Name == "publication-gate" && job.Status == "manual" && !job.AllowFailure {
				gate = job.ID
			}
		}

		return ready && gate != 0
	})

	return gate
}

func waitGitLabParentSuccess(t *testing.T, ctx context.Context, client *gitlabapi.Client, project string, pipeline int64) {
	t.Helper()
	awaitGitLabObservation(t, ctx, func() bool {
		run, _, err := client.Pipelines.GetPipeline(project, pipeline, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == "failed" || run.Status == "canceled" {
			t.Fatalf("release parent %d ended %s", pipeline, run.Status)
		}

		return run.Status == "success"
	})
}

func awaitGitLabObservation(t *testing.T, ctx context.Context, observe func() bool) {
	t.Helper()
	for !observe() {
		select {
		case <-ctx.Done():
			t.Fatal("GitLab observation did not reach its expected state within the bounded wait")
		case <-time.After(2 * time.Second):
		}
	}
}

const gitlabSerializationChild = `stages: [prepare, publish]
default:
  image: ` + livetest.ProbeImage + `
  cache: []
variables:
  GIT_STRATEGY: empty
prepared:
  stage: prepare
  script: 'true'
publication-gate:
  stage: publish
  when: manual
  allow_failure: false
  script: 'true'
`
