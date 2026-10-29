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

func TestForgejoMigrationShellPullRequest(t *testing.T) {
	root := os.Getenv("FORGE_CI_LIVE_MIGRATION_CHECKOUT")
	if root == "" {
		t.Skip("explicit read-only migration checkout required")
	}

	target := Accept(t, provider.ForgeForgejo)
	engine := NewBareScratchRepo(t, target, "migration-shell-engine", "sha1", false)
	repo := NewBareScratchRepo(t, target, "migration-shell-pr", "sha1", false)
	digest, _ := readLayerBinary(t, reporoot.Path(t))
	sha, _, _ := nativeSnapshotTree(t, target, engine, snapshotCandidateFiles(t))
	nativeSnapshotPublishEngine(t, target, engine)
	consumer := OpenConsumer(t, target, root)
	original := consumer.PushSnapshot(t, repo)
	t.Logf("migration source snapshot=%s candidate=%s", original, sha)

	action := target.BaseURL() + "/" + RepoSlug(target, engine) + "/.forgejo/actions/setup-forge-ci@" + sha
	workflow := migrationPRWorkflow(RunnerLabel(t, target), action, digest)
	CommitFiles(t, target, repo, FileCommit{
		Branch: "main", Message: "test: install scratch-only migration PR unit overlay",
		Files: map[string]string{".forgejo/workflows/migration-pr.yml": workflow},
	})
	feature := CommitFiles(t, target, repo, FileCommit{
		Branch: "main", NewBranch: "migration-pr", Message: "test: exercise migration pull request",
		Files: map[string]string{consumerCanary: consumerCanaryText},
	})
	EnableActions(t, target, repo)
	pr := OpenPullRequest(t, target, repo, "migration-pr", "main", "test: public migration shell unit pilot")
	t.Logf("migration PR opened: repository=%s number=%d head=%s", RepoSlug(target, repo), pr.Number, feature)
	run := WaitForLayerRun(t, target, repo, "migration-pr", "pull_request")
	t.Logf("migration PR result: run=%d status=%s", run.ID, run.Status)

	for _, job := range RunJobs(t, target, repo, run.ID) {
		t.Logf("migration job: id=%d name=%s status=%s", job.ID, job.Name, job.Status)

		if len(job.RunsOn) == 0 || job.Attempt == 0 {
			continue
		}

		log, err := jobLog(t, target, repo, job)
		if err != nil {
			t.Errorf("runner log unavailable: job=%d", job.ID)

			continue
		}

		saveSnapshotEvidence(t, fmt.Sprintf("migration-pr-job-%d.log", job.ID), []byte(redactTargetSecrets(target, log)))
	}

	if run.Status != "success" {
		t.Fatal("migration unit PR failed; retained original application check traces")
	}
}
