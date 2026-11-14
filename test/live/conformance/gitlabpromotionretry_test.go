// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	gitlabapi "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// Unlike the small promotion primitive below, this interrupts a real consumer
// after policy-owned candidate publication, then retries the canceled child.
func TestGitLabRelease_ConsumerInterruptedRecovery(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	forScenarioConsumer(t, provider.ForgeGitLab, "consumer recovery requires a reviewed single-image policy-owned release consumer or a prepared directory", func(t *testing.T, root string) {
		t.Helper()
		runGitLabReleaseScenario(t, root, gitlabInterruptedConsumerRelease)
	})
}

func recoverGitLabConsumerAfterCandidate(t *testing.T, ctx context.Context, client *gitlabapi.Client, target livetest.Target, repo, publication, requestTag, requestSHA, finalTag, previousDigest string) (*gitlabapi.Pipeline, int64) {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	project := livetest.RepoSlug(target, repo)
	// ImageDigest fatals on an uncertain registry read; only a proven missing
	// tag becomes Found=false. Capture presence independently of its digest.
	latestDigest, latestFound := livetest.ImageDigestContext(ctx, t, target, publication, "latest")
	initialLatest := livetest.GitLabRecoveryTagState{Digest: latestDigest, Found: latestFound}

	var (
		parent          *gitlabapi.Pipeline
		child           int64
		candidateDigest string
		publisher       int64
		checkpoint      int64
	)

	preparedJobs := map[string]int64{}

	awaitGitLabObservation(t, ctx, func() bool {
		if parent == nil {
			runs, _, err := client.Pipelines.ListProjectPipelines(project, &gitlabapi.ListProjectPipelinesOptions{Ref: new(requestTag)}, gitlabapi.WithContext(ctx))
			if err != nil || len(runs) > 1 {
				t.Fatalf("locate the recovery request pipeline: %v; count=%d", err, len(runs))
			}

			if len(runs) == 0 {
				return false
			}

			parent, err = livetest.PollGitLabPipeline(ctx, client, project, runs[0].ID)
			if err != nil || parent == nil || parent.SHA != requestSHA || parent.Source != "push" {
				t.Fatalf("recovery pipeline is not bound to its signed tag event: %v", err)
			}
		}

		if child == 0 {
			bridges, _, err := client.Jobs.ListPipelineBridges(project, parent.ID, nil, gitlabapi.WithContext(ctx))
			if err != nil || len(bridges) > 1 {
				t.Fatalf("locate recovery child pipeline: %v", err)
			}

			if len(bridges) == 0 || bridges[0].DownstreamPipeline == nil {
				// A parent GitLab failed before its child, as for a refused
				// configuration, never gets one: say why now.
				current, readErr := livetest.PollGitLabPipeline(ctx, client, project, parent.ID)
				if readErr == nil && (current.Status == "failed" || current.Status == "canceled") {
					livetest.LogGitLabPipelineFailure(t, ctx, client, project, current)
					t.Fatalf("recovery parent %d ended %s before its child; GitLab's account is logged above", current.ID, current.Status)
				}

				return false
			}

			child = bridges[0].DownstreamPipeline.ID

			owned, readErr := livetest.PollGitLabPipeline(ctx, client, project, child)
			if readErr != nil || owned == nil || owned.SHA != requestSHA || owned.Ref != requestTag || owned.Source != "parent_pipeline" {
				t.Fatalf("recovery child is not bound to the original request: %v", readErr)
			}

			t.Logf("consumer recovery: parent %d child %d; waiting for the candidate push", parent.ID, child)
		}

		jobs, _, err := client.Jobs.ListPipelineJobs(project, child, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}

		for _, job := range jobs {
			if job.Status == "success" && (strings.HasPrefix(job.Name, "Prepare:") || strings.HasPrefix(job.Name, "Images:") || job.Name == "Build: release files") {
				preparedJobs[job.Name] = job.ID
			}
		}

		publisher, checkpoint, err = livetest.GitLabRecoveryObservation(jobs)
		if err != nil {
			t.Log(gitlabTrialTrace(t, ctx, client, project, child))
			t.Fatal(err)
		}

		if checkpoint == 0 {
			return false
		}

		trace, _, err := client.Jobs.GetTraceFile(project, checkpoint, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}

		body, err := io.ReadAll(io.LimitReader(trace, 1<<20))
		if err != nil {
			t.Fatal(err)
		}

		return gitlabRecoveryCheckpointReady(string(body))
	})

	receiptURL := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/artifacts/_release-images/dist/release-images.json", target.BaseURL(), url.PathEscape(project), publisher)
	receipt := livetest.DownloadContext(ctx, t, target, receiptURL, 1<<20)

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := livetest.GitLabRecoveryCandidate(receipt, registry+"/"+strings.ToLower(project), finalTag)
	if err != nil {
		t.Fatal(err)
	}

	candidateDigest = candidate.Digest
	if digest, found := livetest.ImageDigest(t, target, repo, "staging-"+finalTag); !found || digest != candidateDigest { //nolint:contextcheck // bounded by test owner.
		t.Fatal("published staging tag does not match the immutable policy receipt")
	}

	if _, found := livetest.ImageDigest(t, target, repo, finalTag); found { //nolint:contextcheck // bounded by test owner.
		t.Fatal("source registry contains a final tag before policy promotion")
	}

	prepared, _, err := client.Tags.GetTag(project, finalTag, gitlabapi.WithContext(ctx))
	if err != nil || prepared == nil || prepared.Commit == nil || preparedJobs["Prepare: release commit and tag"] == 0 || preparedJobs["Build: release files"] == 0 {
		t.Fatalf("candidate push lacks its completed release preparation: %v", err)
	}

	preparedSHA := prepared.Commit.ID

	preparedBranch, _, err := client.Branches.GetBranch(project, "main", gitlabapi.WithContext(ctx))
	if err != nil || preparedBranch == nil || preparedBranch.Commit == nil || preparedBranch.Commit.ID != preparedSHA {
		t.Fatalf("prepared release commit is not the release branch head: %v", err)
	}

	t.Logf("consumer recovery: candidate %s from publication job %d is present; canceling child %d at owned checkpoint %d before signing", candidateDigest, publisher, child, checkpoint)

	if _, _, err = client.Pipelines.CancelPipelineBuild(project, child, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	awaitGitLabObservation(t, ctx, func() bool {
		run, pollErr := livetest.PollGitLabPipeline(ctx, client, project, child)
		if pollErr != nil {
			t.Fatal(pollErr)
		}

		return run.Status == "canceled"
	})

	stopped, _, err := client.Jobs.GetJob(project, checkpoint, gitlabapi.WithContext(ctx))
	if err != nil || stopped == nil || stopped.Status != "canceled" {
		t.Fatalf("owned checkpoint did not cancel with the original child: %v", err)
	}

	releases, _, err := client.Releases.ListReleases(project, nil, gitlabapi.WithContext(ctx))
	if err != nil || len(releases) != 0 {
		t.Fatalf("interrupted consumer published a release: %v", err)
	}

	if digest, found := livetest.ImageDigest(t, target, repo, "staging-"+finalTag); !found || digest != candidateDigest { //nolint:contextcheck // registry helper derives a bounded context from the test owner.
		t.Fatal("cancellation did not retain the pushed candidate digest")
	}

	if _, found := livetest.ImageDigest(t, target, publication, finalTag); found { //nolint:contextcheck // bounded by test owner.
		t.Fatal("interrupted consumer published the new final tag")
	}

	prior := map[string]livetest.GitLabRecoveryTagState{"latest": initialLatest, "v0.0.0": {Digest: previousDigest, Found: true}}

	for _, tag := range []string{"latest", "v0.0.0"} {
		digest, found := livetest.ImageDigest(t, target, publication, tag) //nolint:contextcheck // registry helper derives a bounded context from the test owner.
		if err := livetest.CheckGitLabRecoveryTag(tag, livetest.GitLabRecoveryTagState{Digest: digest, Found: found}, prior[tag], nil); err != nil {
			t.Fatal(err)
		}
	}

	t.Logf("consumer recovery: no release published and prior tags preserved; retrying child %d", child)

	if _, _, err = client.ProjectVariables.UpdateVariable(project, livetest.GitLabRecoveryPauseVariable, &gitlabapi.UpdateProjectVariableOptions{Value: new("false")}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	if _, _, err = client.Pipelines.RetryPipelineBuild(project, child, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	awaitGitLabObservation(t, ctx, func() bool {
		run, pollErr := livetest.PollGitLabPipeline(ctx, client, project, child)
		if pollErr != nil {
			t.Fatal(pollErr)
		}

		if run.SHA != requestSHA || run.Ref != requestTag || run.Source != "parent_pipeline" {
			t.Fatal("retry changed the original child's request identity")
		}

		if run.Status == "failed" {
			livetest.LogGitLabPipelineFailure(t, ctx, client, project, run)
			t.Log(gitlabTrialTrace(t, ctx, client, project, child))
			t.Fatal("consumer retry failed")
		}

		return run.Status == "success"
	})

	after, _, err := client.Tags.GetTag(project, finalTag, gitlabapi.WithContext(ctx))
	if err != nil || after == nil || after.Commit == nil || after.Commit.ID != preparedSHA {
		t.Fatalf("retry changed the prepared release tag: %v", err)
	}

	afterBranch, _, err := client.Branches.GetBranch(project, "main", gitlabapi.WithContext(ctx))
	if err != nil || afterBranch == nil || afterBranch.Commit == nil || afterBranch.Commit.ID != preparedSHA {
		t.Fatalf("retry duplicated or changed the prepared release commit: %v", err)
	}

	jobs, _, err := client.Jobs.ListPipelineJobs(project, child, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	for name, id := range preparedJobs {
		found := false
		for _, job := range jobs {
			found = found || (job.Name == name && job.ID == id && job.Status == "success")
		}

		if !found {
			t.Fatalf("retry reran or lost successful preparation job %s", name)
		}
	}

	checkpointRetried := false
	for _, job := range jobs {
		checkpointRetried = checkpointRetried || (job.Name == livetest.GitLabRecoveryCheckpoint && job.ID != checkpoint && job.Status == "success")
	}

	if !checkpointRetried {
		t.Fatal("same-child retry did not clear and complete a new checkpoint attempt")
	}

	if !bytes.Equal(receipt, livetest.DownloadContext(ctx, t, target, receiptURL, 1<<20)) {
		t.Fatal("retry changed the original policy candidate receipt")
	}

	expected := livetest.GitLabRecoveryPublicationDigests(candidate, finalTag, previousDigest, initialLatest)
	for _, tag := range []string{finalTag, "latest", "v0.0.0"} {
		digest, found := livetest.ImageDigest(t, target, publication, tag) //nolint:contextcheck // registry helper derives a bounded context from the test owner.
		if err := livetest.CheckGitLabRecoveryTag(tag, livetest.GitLabRecoveryTagState{Digest: digest, Found: found}, expected[tag], nil); err != nil {
			t.Fatal(err)
		}
	}

	t.Logf("consumer parent %d child %d: canceled after candidate %s; same-version retry preserved preparation and the prior image tag, then published the original versioned digest; latest %+v follows the admitted receipt", parent.ID, child, candidateDigest, expected["latest"])

	return parent, child
}

// Fail after a real registry write, retain the failed job's native journal,
// then retry the same CLI promotion in a clean job. This isolates recovery from
// toolchain/signing acceptance, which the signed consumer scenarios exercise.
func TestGitLabRelease_PromotionRetryAfterRegistryWrite(t *testing.T) {
	registerLaneScenario(t)

	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "promotion recovery requires a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	repo := livetest.NewPublicScratchRepo(t, target, "promotion-retry", livetest.ReadProbeAssetAnonymously)
	project := livetest.RepoSlug(target, repo)

	const candidateTag, finalTag, assetTag = "staging-v0.0.1", "v0.0.1", "v0.0.9-probe"

	image := livetest.PushImage(t, target, repo, candidateTag)
	imagePath := strings.TrimSuffix(image.Ref, ":"+candidateTag)

	livetest.PrepareTag(t, target, repo, assetTag)
	livetest.PublishBinaryAsset(t, target, repo, assetTag, t.TempDir())

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(livetest.Context(t), 8*time.Minute)
	defer cancel()

	const faultVariable = "FIXTURE_FAIL_AFTER_PROMOTION"
	if _, _, err = client.ProjectVariables.CreateVariable(project, &gitlabapi.CreateProjectVariableOptions{
		Key: gitlabapi.Ptr(faultVariable), Value: new("true"),
	}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	ledger, err := json.Marshal([]imageledger.Entry{{
		ImageKind: imageledger.ImageKindRelease, Role: "distroless", Ref: imagePath + "@" + image.Digest,
		Digest: image.Digest, CandidateTag: image.Ref, FinalTag: imagePath + ":" + finalTag,
	}})
	if err != nil {
		t.Fatal(err)
	}

	prelude := livetest.ProbePrelude(target, livetest.ReleaseAssetURL(t, target, repo, assetTag, "forge-ci"))
	commit := gitlabPolicyCommit(t, ctx, client, project, ".gitlab-ci.yml", gitlabPromotionRetryWorkflow(t, prelude, string(ledger)), gitlabapi.FileCreate)

	run := waitGitLabTagPipeline(t, ctx, client, project, "main", commit.ID)
	if run.Status != "failed" {
		t.Fatalf("post-write fault did not fail the pipeline: %s", run.Status)
	}

	jobs, _, err := client.Jobs.ListPipelineJobs(project, run.ID, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	failed := failedGitLabPromotionJob(t, jobs)
	if failed == 0 {
		t.Log(gitlabTrialTrace(t, ctx, client, project, run.ID))
		t.Fatal("failed promotion did not retain its native artifact")
	}

	if digest, exists := livetest.ImageDigest(t, target, repo, finalTag); !exists || digest != image.Digest {
		t.Log(gitlabTrialTrace(t, ctx, client, project, run.ID))
		t.Fatal("fixture failed before the registry write")
	}

	if digest, exists := livetest.ImageDigest(t, target, repo, candidateTag); !exists || digest != image.Digest {
		t.Fatal("failed promotion removed its retry source")
	}

	artifact := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/artifacts/_release-promote/image-promotions.jsonl", target.BaseURL(), url.PathEscape(project), failed)
	original := livetest.Download(t, target, artifact, 1<<20)

	records, err := imageledger.ParsePromotionJournal(original)
	if err != nil || len(records) != 1 || records[0].FinalExisted || records[0].FinalTag != imagePath+":"+finalTag {
		t.Fatalf("failed job lost pre-write journal state: %v, records=%+v", err, records)
	}
	// An OCI digest reference may retain the descriptive candidate tag. Judge
	// the repository and digest, not which equivalent string spelling was used.
	source, err := name.NewDigest(records[0].SourceRef)
	if err != nil || source.Context().Name() != imagePath || source.DigestStr() != image.Digest {
		t.Fatalf("journal source does not bind the promoted image: %s", records[0].SourceRef)
	}

	if _, _, err = client.ProjectVariables.UpdateVariable(project, faultVariable, &gitlabapi.UpdateProjectVariableOptions{Value: new("false")}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	retried, _, err := client.Jobs.RetryJob(project, failed, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	awaitGitLabObservation(t, ctx, func() bool {
		job, _, readErr := client.Jobs.GetJob(project, retried.ID, gitlabapi.WithContext(ctx))
		if readErr != nil {
			t.Fatal(readErr)
		}

		if job.Status == "failed" || job.Status == "canceled" {
			t.Log(gitlabTrialTrace(t, ctx, client, project, run.ID))
			t.Fatalf("promotion retry ended %s", job.Status)
		}

		return job.Status == "success"
	})
	waitGitLabParentSuccess(t, ctx, client, project, run.ID)

	if digest, exists := livetest.ImageDigest(t, target, repo, finalTag); !exists || digest != image.Digest {
		t.Fatal("retry changed the immutable promoted digest")
	}

	if !bytes.Equal(original, livetest.Download(t, target, artifact, 1<<20)) {
		t.Fatal("retry replaced the first failed job's original journal")
	}

	t.Logf("pipeline %d: post-write failure job %d retained its journal and blocked publication/cleanup; retry job %d converged to %s", run.ID, failed, retried.ID, image.Digest)
}

func failedGitLabPromotionJob(t *testing.T, jobs []*gitlabapi.Job) int64 {
	t.Helper()

	if len(jobs) != 3 {
		t.Fatalf("recovery pipeline has %d jobs, want promote/publish/cleanup", len(jobs))
	}

	var failed int64

	for _, job := range jobs {
		if job.Name == "Publish: images" {
			if job.Status == "failed" && job.ArtifactsFile.Size > 0 {
				failed = job.ID
			}
		} else if job.Status != "skipped" {
			t.Fatalf("downstream job %s ran after failed promotion: %s", job.Name, job.Status)
		}
	}

	return failed
}

func gitlabPromotionRetryWorkflow(t *testing.T, prelude, ledger string) string {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(reporoot.ReadFile(t, "templates/release-jobs.yml")))

	var spec any
	if err := decoder.Decode(&spec); err != nil {
		t.Fatal(err)
	}

	var jobs map[string]yaml.Node
	if err := decoder.Decode(&jobs); err != nil {
		t.Fatal(err)
	}

	var promotion struct {
		AfterScript []string       `yaml:"after_script"`
		Artifacts   map[string]any `yaml:"artifacts"`
	}

	promotionNode := jobs["Publish: images"]
	if err := promotionNode.Decode(&promotion); err != nil {
		t.Fatal(err)
	}

	pipeline := map[string]any{
		"stages":    []string{"promote", "publish", "cleanup"},
		"default":   map[string]any{"image": livetest.ProbeImage},
		"variables": map[string]string{"GIT_STRATEGY": "empty", "LEDGER_JSON": ledger},
		"Publish: images": map[string]any{
			"stage": "promote", "script": []string{prelude, gitlabPromotionRetryScript},
			"after_script": promotion.AfterScript,
			"artifacts":    promotion.Artifacts,
		},
		"Publish: release": map[string]any{"stage": "publish", "needs": []string{"Publish: images"}, "script": "test -s _release-promote/image-promotions.jsonl"},
		"Publish: tidy up": map[string]any{"stage": "cleanup", "needs": []string{"Publish: release"}, "script": "true"},
	}

	body, err := yaml.Marshal(pipeline)
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

const gitlabPromotionRetryScript = `set -euo pipefail
umask 077
mkdir dist
printf '%s\n' "$LEDGER_JSON" > dist/release-images.json
printf '%s' "$CI_REGISTRY_PASSWORD" | ./forge-ci container release-images promote \
  --ledger dist/release-images.json --tag v0.0.1 --server-url "$CI_SERVER_URL" \
  --repository "$CI_PROJECT_PATH" --registry "$CI_REGISTRY" \
  --expected-image-repository "$CI_REGISTRY_IMAGE" --registry-username "$CI_REGISTRY_USER" \
  --registry-password-file - --journal "$CI_PROJECT_DIR/_release-promote/image-promotions.jsonl"
if [ "$FIXTURE_FAIL_AFTER_PROMOTION" = true ]; then
  echo 'fixture: registry write completed; fail before publication'
  exit 42
fi
`
