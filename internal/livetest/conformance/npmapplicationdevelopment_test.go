// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

func TestForgejoLayer_NPMApplicationDevelopment(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" || os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_IMAGE") == "" {
		t.Skip("name npm application source, pinned image and installed-package oracle")
	}
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		if visibility.Private {
			t.Fatal("npm application acceptance requires public scratch repositories")
		}
	}
	if livetest.DemoName() != "" {
		t.Fatal("npm application acceptance requires disposable scratch repositories")
	}
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner, livetest.NeedsPackages) {
		livetest.NotCoveredf(t, "npm application needs runner and packages")
	}
	target := livetest.Accept(t, provider.ForgeForgejo)
	if livetest.RunnerArch(t, target) != "amd64" {
		t.Fatal("npm application requires native linux/amd64")
	}
	recipe, err := filepath.Abs("../../../test/live/demos/hello-npm-service/consumer")
	if err != nil {
		t.Fatal(err)
	}
	c := livetest.NPMApplicationFixtureConfig{RecipeDirectory: recipe, NodeImage: os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_IMAGE"), Oracle: os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_ORACLE"), Branch: os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_BRANCH")}
	consumer := livetest.NewNPMApplicationFixture(t, livetest.OpenConsumer(t, target, root), c, true)
	layer, repo := newConsumerRepos(t, target, consumer, "npm-application-development", false)
	plan, err := layer.PlanConsumer(consumer.Workflows(t), livetest.ConsumerFacts{Inputs: map[string]string{"forge-ci-repository": layer.Repository}})
	if err != nil {
		t.Fatal(err)
	}
	main := consumer.Push(t, repo, plan)
	head := consumer.PushChange(t, repo, c.Branch, main)
	label := privilegedRunner(t, target)
	scenario := layerScenario{target: target, repo: repo, layer: layer, label: label}
	defer func() {
		scenario.requireBranches(t, map[string]string{"main": main, c.Branch: head})
		scenario.requireNone(t, "/tags", "/releases")
	}()
	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}
	variables, err := livetest.MavenServiceDevelopmentVariables(plan.Variables, map[string]string{
		layerRunnerVariable: runnerLabelsJSON(t, livetest.RunnerLabel(t, target)), containerRunnerVariable: runnerLabelsJSON(t, label),
		"FORGE_CI_REPOSITORY": layer.Repository, "SERVICE_IMAGE_REPOSITORY": registry + "/" + strings.ToLower(livetest.RepoSlug(target, repo)), "DEVELOPMENT_REGISTRY_USERNAME": target.CredentialUsername,
	})
	if err != nil {
		t.Fatal(err)
	}
	livetest.EnableActions(t, target, repo)
	for name, value := range variables {
		livetest.SetRepoVariable(t, target, repo, name, value)
	}
	livetest.SetRepoSecret(t, target, repo, "DEVELOPMENT_REGISTRY_TOKEN", target.Token)
	livetest.DispatchConsumerDevelopment(t, target, repo)
	run := livetest.WaitForConsumerRun(t, target, repo, "development.yml", "workflow_dispatch", "")
	jobs := livetest.RunJobs(t, target, repo, run.ID)
	scenario.logJobs(t, jobs)
	if run.Status != jobSuccess {
		t.Fatalf("npm application run %d ended %s; inspect original Containerfile snapshot first (known Forgejo upstream #12191)", run.ID, run.Status)
	}
	confirmed := false
	for _, job := range jobs {
		if strings.Contains(livetest.JobLog(t, target, repo, job), "NPM_APPLICATION_ORACLE_OK:"+head) {
			confirmed = true
		}
	}
	if !confirmed {
		t.Fatal("no installed-package oracle evidence for selected source")
	}
	retained := false
	for _, artifact := range livetest.RunArtifacts(t, target, repo, run.ID) {
		if artifact.Name == "npm-application-"+head && !artifact.Expired && artifact.SizeInBytes > 0 && artifact.SizeInBytes <= 256<<20 {
			retained = true
		}
	}
	if !retained {
		t.Fatal("missing bounded retained npm package artifact")
	}
}
