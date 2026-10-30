// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// Independent of the PR/release trial: only a native development push is judged.
func TestForgejoLayer_ConsumerDevelopment(t *testing.T) {
	if os.Getenv("FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE") != "" {
		runForgejoMavenServiceDevelopment(t)
		return
	}
	runForgejoConsumerDevelopment(t, packageDevelopmentAcceptance())
}

func runForgejoConsumerDevelopment(t *testing.T, acceptance developmentAcceptance) {
	t.Helper()

	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skipf("name a clean development consumer with FORGE_CI_LIVE_CONSUMER and a pinned %s", acceptance.imageVariable)
	}

	if livetest.DemoName() != "" {
		t.Fatal("the development trial requires disposable scratch repositories")
	}

	image := os.Getenv(acceptance.imageVariable)
	if !regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatalf("%s must name the job image by SHA-256 digest", acceptance.imageVariable)
	}

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner) {
				livetest.NotCoveredf(t, "the development consumer needs a Forgejo runner")
			}

			target := livetest.Accept(t, provider.ForgeForgejo)

			// The runner the development job is expected on.
			jobLabel := livetest.RunnerLabel(t, target)
			if acceptance.buildsImages {
				jobLabel = privilegedRunner(t, target)
			}

			if acceptance.checkPublication != nil && livetest.RunnerArch(t, target) != "amd64" {
				t.Fatal("the publishing development consumer requires a native linux/amd64 runner")
			}

			consumer := livetest.OpenConsumer(t, target, root)

			const workflow = "development.yml"

			workflows := consumer.Workflows(t)
			if _, found := workflows[workflow]; !found {
				t.Fatal("the consumer must supply .forgejo/workflows/development.yml")
			}

			layer, repo := newConsumerRepos(t, target, consumer, "consumer-development", visibility.Private)

			plan, err := layer.PlanConsumer(workflows, livetest.ConsumerFacts{
				Inputs: map[string]string{"forge-ci-repository": layer.Repository},
			})
			if err != nil {
				t.Fatal(err)
			}

			main := consumer.Push(t, repo, plan)
			requireObjectFormats(t, consumer.ObjectFormat, main, layer.Commit)
			// The branch listing can lag a successful push. Retry only absence;
			// API failures and a visible branch at the wrong commit must fail.
			if err := retry.Run(livetest.Context(t), nil, 30, 2*time.Second, func() error {
				before, found := livetest.Branches(t, target, repo)["main"]
				if !found {
					return fmt.Errorf("main is not visible after push: %w", errs.ErrDependencyUnavailable)
				}

				if before != main {
					return retry.Permanent(fmt.Errorf("main before development = %s, want %s: %w", before, main, errs.ErrValidation))
				}

				return nil
			}, retry.WithBackoff(retry.Constant)); err != nil {
				t.Fatal(err)
			}

			if before := livetest.BranchCommitSHA(t, target, repo, "main"); before != main {
				t.Fatalf("main before development = %s, want %s", before, main)
			}
			// Judge these even if the workflow or archive acceptance fails.
			defer func() {
				if after := livetest.BranchCommitSHA(t, target, repo, "main"); after != main {
					t.Errorf("development advanced main: before=%s after=%s", main, after)
				}

				if livetest.RepoCount(t, target, repo, "/tags") != 0 {
					t.Error("development created tags")
				}

				if releases := livetest.Releases(t, target, repo); len(releases) != 0 {
					t.Errorf("development created %d releases", len(releases))
				}
			}()

			label := livetest.RunnerLabel(t, target)
			livetest.EnableActions(t, target, repo)

			for name, value := range plan.Variables {
				livetest.SetRepoVariable(t, target, repo, name, value)
			}

			livetest.SetRepoVariable(t, target, repo, layerRunnerVariable, runnerLabelsJSON(t, label))
			livetest.SetRepoVariable(t, target, repo, "FORGE_CI_REPOSITORY", layer.Repository)
			livetest.SetRepoVariable(t, target, repo, acceptance.imageVariable, image)

			if acceptance.buildsImages {
				livetest.SetRepoVariable(t, target, repo, containerRunnerVariable, runnerLabelsJSON(t, jobLabel))
			}

			if acceptance.checkPublication != nil {
				livetest.SetRepoVariable(t, target, repo, "DEVELOPMENT_REGISTRY_USERNAME", target.CredentialUsername)
				livetest.SetRepoSecret(t, target, repo, "DEVELOPMENT_REGISTRY_TOKEN", target.Token)
			}

			head := consumer.PushChange(t, repo, "development", main)
			t.Logf("consumer %s at %s; development %s; layer %s; image %s", root, consumer.Commit, head, layer.Commit, image)
			run := livetest.WaitForConsumerRun(t, target, repo, workflow, "push", "")
			jobs := livetest.RunJobs(t, target, repo, run.ID)
			scenario := layerScenario{target: target, repo: repo, layer: layer, label: label}
			scenario.logJobs(t, jobs)

			if run.Status != jobSuccess || run.Event != "push" || run.TriggerEvent != "push" || run.WorkflowID != workflow {
				t.Fatalf("want successful native development push, got %+v", run)
			}

			if runs := livetest.WorkflowRuns(t, target, repo); len(runs) != 1 || runs[0].ID != run.ID {
				t.Fatalf("want only the development run, got %+v", runs)
			}

			assertLayerJobs(t, jobs, run.ID, jobLabel, acceptance.jobName)

			if !strings.Contains(livetest.JobLog(t, target, repo, jobs[0]), acceptance.smokeMarker+head) {
				t.Fatal("development job did not confirm smoke at the expected source commit")
			}

			artifacts := livetest.RunArtifacts(t, target, repo, run.ID)
			if len(artifacts) != 1 || artifacts[0].Name != acceptance.artifactPrefix+head || artifacts[0].Expired || artifacts[0].SizeInBytes <= 0 || artifacts[0].SizeInBytes > acceptance.maxArchive {
				t.Fatalf("want one bounded exact-source development artifact, got %+v", artifacts)
			}

			address := fmt.Sprintf("%s/api/v1/repos/%s/%s/actions/artifacts/%d/zip", target.BaseURL(), url.PathEscape(target.Owner), url.PathEscape(repo), artifacts[0].ID)

			body := livetest.Download(t, target, address, acceptance.maxArchive)
			if acceptance.checkArchive != nil {
				acceptance.checkArchive(t, body, head)
			}

			if acceptance.checkPublication != nil {
				acceptance.checkPublication(t, body, head, target, repo)
			}
		})
	}
}
