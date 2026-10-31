// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	gitlabapi "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const gitlabCandidatePauseVariable = "FIXTURE_PAUSE_AFTER_CANDIDATE"

// Unlike the small promotion primitive below, this interrupts a real consumer
// after its build pushed an image, then retries the canceled child pipeline.
func TestGitLabRelease_ConsumerInterruptedRecovery(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		livetest.NotCoveredf(t, "consumer recovery requires a reviewed image-producing consumer with the candidate pause checkpoint")
	}

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabReleaseScenario(t, root, gitlabInterruptedConsumerRelease, visibility.Private)
		})
	}
}

func recoverGitLabConsumerAfterCandidate(t *testing.T, ctx context.Context, client *gitlabapi.Client, target livetest.Target, repo, publication, requestTag, requestSHA, finalTag, previousDigest string) (*gitlabapi.Pipeline, int64) {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	project := livetest.RepoSlug(target, repo)

	var (
		parent          *gitlabapi.Pipeline
		child           int64
		candidateDigest string
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

			parent, _, err = client.Pipelines.GetPipeline(project, runs[0].ID, gitlabapi.WithContext(ctx))
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
				return false
			}

			child = bridges[0].DownstreamPipeline.ID
			t.Logf("consumer recovery: parent %d child %d; waiting for the candidate push", parent.ID, child)
		}

		jobs, _, err := client.Jobs.ListPipelineJobs(project, child, nil, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}

		buildRunning := false

		for _, job := range jobs {
			if strings.HasPrefix(job.Name, "Prepare:") && job.Status == "success" {
				preparedJobs[job.Name] = job.ID
			}

			if job.Status == "failed" || job.Status == "canceled" {
				t.Log(gitlabTrialTrace(t, ctx, client, project, child))
				t.Fatalf("consumer failed before the interruption checkpoint: %s", job.Name)
			}

			if job.Name == "Build: release files" {
				if job.Status == "success" {
					t.Fatal("consumer did not hold the candidate-push checkpoint")
				}

				if job.Status == "running" {
					buildRunning = true
				}
			}
		}

		if buildRunning {
			var found bool

			candidateDigest, found = livetest.ImageDigest(t, target, repo, finalTag) //nolint:contextcheck // registry helper derives a bounded context from the test owner.

			return found
		}

		return false
	})

	prepared, _, err := client.Tags.GetTag(project, finalTag, gitlabapi.WithContext(ctx))
	if err != nil || prepared == nil || prepared.Commit == nil || len(preparedJobs) == 0 {
		t.Fatalf("candidate push lacks its completed release preparation: %v", err)
	}

	preparedSHA := prepared.Commit.ID

	t.Logf("consumer recovery: candidate %s is present; canceling child %d before build completion", candidateDigest, child)

	if _, _, err = client.Pipelines.CancelPipelineBuild(project, child, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	awaitGitLabObservation(t, ctx, func() bool {
		run, _, pollErr := client.Pipelines.GetPipeline(project, child, gitlabapi.WithContext(ctx))
		if pollErr != nil {
			t.Fatal(pollErr)
		}

		return run.Status == "canceled"
	})

	releases, _, err := client.Releases.ListReleases(project, nil, gitlabapi.WithContext(ctx))
	if err != nil || len(releases) != 0 {
		t.Fatalf("interrupted consumer published a release: %v", err)
	}

	if digest, found := livetest.ImageDigest(t, target, repo, finalTag); !found || digest != candidateDigest { //nolint:contextcheck // registry helper derives a bounded context from the test owner.
		t.Fatal("cancellation did not retain the pushed candidate digest")
	}

	for _, tag := range []string{"latest", "v0.0.0"} {
		if digest, found := livetest.ImageDigest(t, target, publication, tag); !found || digest != previousDigest { //nolint:contextcheck // registry helper derives a bounded context from the test owner.
			t.Fatalf("interrupted consumer changed prior publication tag %s", tag)
		}
	}

	t.Logf("consumer recovery: no release published and prior tags preserved; retrying child %d", child)

	if _, _, err = client.ProjectVariables.UpdateVariable(project, gitlabCandidatePauseVariable, &gitlabapi.UpdateProjectVariableOptions{Value: new("false")}, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	if _, _, err = client.Pipelines.RetryPipelineBuild(project, child, gitlabapi.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	awaitGitLabObservation(t, ctx, func() bool {
		run, _, pollErr := client.Pipelines.GetPipeline(project, child, gitlabapi.WithContext(ctx))
		if pollErr != nil {
			t.Fatal(pollErr)
		}

		if run.Status == "failed" {
			t.Log(gitlabTrialTrace(t, ctx, client, project, child))
			t.Fatal("consumer retry failed")
		}

		return run.Status == "success"
	})

	after, _, err := client.Tags.GetTag(project, finalTag, gitlabapi.WithContext(ctx))
	if err != nil || after == nil || after.Commit == nil || after.Commit.ID != preparedSHA {
		t.Fatalf("retry changed the prepared release tag: %v", err)
	}

	jobs, _, err := client.Jobs.ListPipelineJobs(project, child, nil, gitlabapi.WithContext(ctx))
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

	for tag, want := range map[string]string{finalTag: candidateDigest, "latest": candidateDigest, "v0.0.0": previousDigest} {
		if digest, found := livetest.ImageDigest(t, target, publication, tag); !found || digest != want { //nolint:contextcheck // registry helper derives a bounded context from the test owner.
			t.Fatalf("recovered publication tag %s has digest %s, want %s", tag, digest, want)
		}
	}

	t.Logf("consumer parent %d child %d: canceled after candidate %s; same-version retry preserved preparation and the prior image tag, then published the original digest and advanced latest", parent.ID, child, candidateDigest)

	return parent, child
}

// Fail after a real registry write, retain the failed job's native journal,
// then retry the same CLI promotion in a clean job. This isolates recovery from
// toolchain/signing acceptance, which the signed consumer scenarios exercise.
func TestGitLabRelease_PromotionRetryAfterRegistryWrite(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "promotion recovery requires a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	repo := livetest.NewScratchRepo(t, target, "promotion-retry")
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

	var shared struct {
		AfterScript []string `yaml:"after_script"`
	}

	sharedNode := jobs[".forge-ci-release"]
	if err := sharedNode.Decode(&shared); err != nil {
		t.Fatal(err)
	}

	var promotion struct {
		Artifacts map[string]any `yaml:"artifacts"`
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
			"after_script": shared.AfterScript,
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
