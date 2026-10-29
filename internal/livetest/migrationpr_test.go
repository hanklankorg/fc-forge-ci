// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"fmt"
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestForgejoConsumerPRUnitChecks(t *testing.T) {
	root := os.Getenv("FORGE_CI_LIVE_CONSUMER")
	if root == "" {
		t.Skip("explicit read-only consumer checkout required")
	}
	settings, err := consumerPRSettings(os.Getenv("FORGE_CI_LIVE_CONSUMER_SETTINGS"))
	if err != nil {
		t.Fatal(err)
	}

	target := Accept(t, provider.ForgeForgejo)
	engine := NewBareScratchRepo(t, target, "consumer-pr-engine", "sha1", false)
	repo := NewBareScratchRepo(t, target, "consumer-pr-units", "sha1", false)
	digest, _ := readLayerBinary(t, reporoot.Path(t))
	sha, _, _ := nativeSnapshotTree(t, target, engine, snapshotCandidateFiles(t))
	nativeSnapshotPublishEngine(t, target, engine)
	consumer := OpenConsumer(t, target, root)
	original := consumer.PushSnapshot(t, repo)
	t.Logf("consumer source snapshot=%s candidate=%s; checkout and original unit checks only", original, sha)

	action := target.BaseURL() + "/" + RepoSlug(target, engine) + "/.forgejo/actions/setup-forge-ci@" + sha
	fixture := publicShellPRFixture(RunnerLabel(t, target), action, digest)
	if config := os.Getenv("FORGE_CI_LIVE_CONSUMER_PR_FIXTURE"); config != "" {
		fixture, err = configureConsumerPRFixture(fixture, config)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Tool privacy is a caller admission, never inferred from source contents.
	fixture.PublicTools = fixture.PublicTools && os.Getenv("FORGE_CI_LIVE_CONSUMER_PUBLIC_TOOLS") == "true"
	workflow, err := consumerPRWorkflow(fixture)
	if err != nil {
		t.Fatal(err)
	}
	CommitFiles(t, target, repo, FileCommit{
		Branch: "main", Message: "test: install scratch-only consumer PR unit overlay",
		Files: map[string]string{".forgejo/workflows/" + settings.Workflow: workflow},
	})
	feature := CommitFiles(t, target, repo, FileCommit{
		Branch: "main", NewBranch: "consumer-pr", Message: "test: exercise consumer pull request",
		Files: map[string]string{consumerCanary: consumerCanaryText},
	})
	EnableActions(t, target, repo)
	for key, value := range settings.ResolvedVariables(sha, feature) {
		SetRepoVariable(t, target, repo, key, value)
	}
	pr := OpenPullRequest(t, target, repo, "consumer-pr", "main", "test: consumer PR unit checks")
	t.Logf("consumer PR opened: repository=%s number=%d head=%s", RepoSlug(target, repo), pr.Number, feature)
	run := WaitForConsumerRun(t, target, repo, settings.Workflow, "pull_request", "")
	t.Logf("consumer PR result: run=%d status=%s", run.ID, run.Status)

	for _, job := range RunJobs(t, target, repo, run.ID) {
		t.Logf("consumer job: id=%d name=%s status=%s", job.ID, job.Name, job.Status)

		if len(job.RunsOn) == 0 || job.Attempt == 0 {
			continue
		}

		log, err := jobLog(t, target, repo, job)
		if err != nil {
			t.Errorf("runner log unavailable: job=%d", job.ID)

			continue
		}

		saveSnapshotEvidence(t, fmt.Sprintf("consumer-pr-job-%d.log", job.ID), []byte(redactTargetSecrets(target, log)))
	}

	if run.Status != "success" {
		t.Fatal("consumer unit PR failed; retained original application check traces")
	}
}
