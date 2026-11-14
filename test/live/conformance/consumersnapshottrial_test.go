// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// TestForgejoLayer_ConsumerSnapshotTrial runs the consumer's ephemeral snapshot
// workflow on a branch push. It supplies no publication or signing credentials.
func TestForgejoLayer_ConsumerSnapshotTrial(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("set FORGE_CI_LIVE_CONSUMER to a clean consumer checkout")
	}

	if livetest.DemoName() != "" {
		t.Fatal("snapshot trials require disposable scratch repositories")
	}

	image := os.Getenv("FORGE_CI_LIVE_SNAPSHOT_JOB_IMAGE")
	if !regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatal("FORGE_CI_LIVE_SNAPSHOT_JOB_IMAGE must be digest-pinned")
	}

	dependencySecrets := map[string]string{}
	if path := os.Getenv("FORGE_CI_LIVE_SNAPSHOT_DEPENDENCY_TOKEN_FILE"); path != "" {
		dependencySecrets["DEPENDENCY_GITHUB_TOKEN"] = livetest.ReadSecretFile(t, "DEPENDENCY_GITHUB_TOKEN", path)
	}

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner) {
				livetest.NotCoveredf(t, "snapshot trial requires a Forgejo runner")
			}

			target := livetest.Accept(t, provider.ForgeForgejo)
			consumer := livetest.OpenConsumer(t, target, root)
			workflows := consumer.Workflows(t)

			const workflow = "snapshot.yml"
			if _, found := workflows[workflow]; !found {
				t.Fatal("consumer must supply .forgejo/workflows/snapshot.yml")
			}

			layer, repo := newConsumerRepos(t, target, consumer, "consumer-snapshot-trial", visibility.Private)

			plan, err := layer.PlanConsumer(workflows, livetest.ConsumerFacts{
				Inputs: map[string]string{"forge-ci-repository": layer.Repository},
			})
			if err != nil {
				t.Fatal(err)
			}

			label := livetest.RunnerLabel(t, target)

			variables, err := livetest.MergeConsumerVariables(plan.Variables, map[string]string{
				layerRunnerVariable:   runnerLabelsJSON(t, label),
				"FORGE_CI_REPOSITORY": layer.Repository,
				"CI_JOB_IMAGE":        image,
			})
			if err != nil {
				t.Fatal(err)
			}

			main := consumer.Push(t, repo, plan)
			requireObjectFormats(t, consumer.ObjectFormat, main, layer.Commit)
			livetest.EnableActions(t, target, repo)

			for name, value := range variables {
				livetest.SetRepoVariable(t, target, repo, name, value)
			}

			for name, value := range dependencySecrets {
				livetest.DeleteSecretsAfter(t, target, repo, []string{name})
				livetest.SetRepoSecret(t, target, repo, name, value)
			}

			packages := livetest.OwnerPackages(t, target)
			head := consumer.PushChange(t, repo, "snapshot-trial", main)
			t.Logf("snapshot consumer=%s source=%s main=%s branch=%s layer=%s", root, consumer.Commit, main, head, layer.Commit)
			run := livetest.WaitForConsumerRun(t, target, repo, workflow, "push", "")
			scenario := layerScenario{target: target, repo: repo, layer: layer, label: label}
			scenario.logJobs(t, livetest.RunJobs(t, target, repo, run.ID), dependencySecrets)
			logArtifacts(t, run, livetest.RunArtifacts(t, target, repo, run.ID))

			if run.Status != jobSuccess {
				t.Errorf("snapshot run %d ended %q, want success", run.ID, run.Status)
			}

			scenario.requireBranches(t, map[string]string{"main": main, "snapshot-trial": head})
			scenario.requireNone(t, "/tags", "/releases", "/keys")
			scenario.requireSamePackages(t, packages)

			if count := livetest.RepoCount(t, target, repo, "/actions/secrets"); count != len(dependencySecrets) {
				t.Errorf("snapshot left %d secrets, want only %d supplied dependency credentials", count, len(dependencySecrets))
			}
		})
	}
}
