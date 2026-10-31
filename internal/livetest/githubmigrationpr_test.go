// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The original consumer workflow with a scratch PR caller and strict Surefire
// evidence. This baseline is separate from the current-engine checkout proof.
func TestGitHubConsumerPRUnitBaseline(t *testing.T) {
	RegisterLiveScenario(t)
	root := os.Getenv("FORGE_CI_LIVE_CONSUMER")
	if root == "" || os.Getenv(GitHubContextEnv) == "" {
		t.Skip("explicit checkout and hosted SDK session required")
	}
	settings, err := consumerPRSettings(os.Getenv("FORGE_CI_LIVE_CONSUMER_SETTINGS"))
	if err != nil {
		t.Fatal(err)
	}
	if kind := os.Getenv("FORGE_CI_LIVE_CONSUMER_REPORT_KIND"); kind != "surefire" {
		t.Fatal("original unit baseline requires explicit report kind surefire")
	}
	expectedJob := os.Getenv("FORGE_CI_LIVE_CONSUMER_EXPECTED_JOB")
	if expectedJob == "" {
		t.Fatal("explicit original workflow job display name required")
	}
	id, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_ID"), 10, 64)
	if err != nil || id <= 0 {
		t.Fatal("admitted organization ID required")
	}
	trial := OpenGitHubTrial(t, id)
	dir := trial.tempDir("consumer-pr")
	consumer, staged, err := githubStage(t.Context(), githubStageGit(), root, trial.tempDir("consumer-objects"), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	originalPath := ".github/workflows/" + settings.Workflow
	original, err := githubWorkflow(t.Context(), consumer, originalPath)
	if err != nil {
		t.Fatal(err)
	}
	// The caller supplies the runtime display name, including matrix expansion.
	// Retention is appended only to a single concrete test job with steps.
	if err := consumerPRTestWorkflow(original); err != nil {
		t.Fatal(err)
	}
	source := consumer.Commit
	if snapshotEvidenceRoot() == "" {
		t.Fatal("configure FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR to retain Java baseline evidence")
	}
	if err := migrationEvidenceDestination(snapshotEvidenceRoot(), root, dir); err != nil {
		t.Fatal(err)
	}
	repo := trial.NewConsumerRepository("consumer-pr-baseline", false)
	for key, value := range settings.Variables {
		if strings.HasPrefix(value, "@") {
			t.Fatal("baseline variables must be literal; this proof has no engine plan")
		}
		trial.SetVariable(repo, key, value)
	}
	retention := `      - name: Retain original Maven test reports
        if: always()
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02
        with:
          name: migration-java-surefire
          path: target/surefire-reports
          if-no-files-found: error
`
	if err := staged.stageFile(t.Context(), "100644", originalPath, original+"\n"+retention); err != nil {
		t.Fatal(err)
	}
	const callerWorkflow = "consumer-pr-baseline.yml"
	if settings.Workflow == callerWorkflow {
		t.Fatal("original workflow must differ from scratch caller")
	}
	if err := staged.stageFile(t.Context(), "100644", ".github/workflows/"+callerWorkflow, fmt.Sprintf(`name: Consumer PR unit baseline
on:
  pull_request:
    branches: [main]
permissions:
  contents: read
  packages: read
jobs:
  original-unit-check:
    uses: %q
`, "./"+originalPath)); err != nil {
		t.Fatal(err)
	}
	trial.commit(dir, "test: snapshot original consumer PR unit workflow", "")
	trial.git(dir, "checkout-index", "--all", "--force")
	main := strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD"))
	trial.push(dir, trial.remote(repo), "HEAD:refs/heads/main")
	trial.waitWorkflows(repo, filepath.Join(dir, ".github", "workflows"))
	head := trial.PushChange(repo, GitHubConsumerTree{Dir: dir, Main: main}, "consumer-pr")
	t.Cleanup(func() { settleBootstrapRuns(t, trial, repo, head, callerWorkflow, "pull_request") })
	trial.OpenPullRequest(repo, "consumer-pr")
	t.Logf("consumer baseline PR opened: repository=%s/%s source=%s main=%s head=%s", trial.org, repo, source, main, head)
	run := trial.WaitRun(repo, callerWorkflow, "pull_request", head, 25*time.Minute)
	t.Logf("consumer baseline PR result: run=%d conclusion=%s url=%s", run.GetID(), run.GetConclusion(), run.GetHTMLURL())
	trial.ReportRun(repo, run)
	if _, err := trial.migrationJavaEvidence(t.Context(), repo, run, "original-unit-check / "+expectedJob, snapshotEvidenceRoot()); err != nil {
		t.Errorf("Java baseline execution/report evidence: %v", err)
	}
	if run.GetConclusion() != "success" {
		t.Fatal("original consumer PR unit workflow failed")
	}
}
