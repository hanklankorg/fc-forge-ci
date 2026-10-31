// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// A native Cargo development push publishes an unsigned image, not a release.
func TestForgejoLayer_ContainerDevelopment(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("name a clean Cargo container-first development consumer with FORGE_CI_LIVE_CONSUMER")
	}

	if livetest.DemoName() != "" {
		t.Fatal("the development trial requires disposable scratch repositories")
	}

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner) {
				livetest.NotCoveredf(t, "the development consumer needs a Forgejo runner")
			}

			target := livetest.Accept(t, provider.ForgeForgejo)
			privileged := privilegedRunner(t, target)

			if livetest.RunnerArch(t, target) != "amd64" {
				t.Fatal("the Cargo fixture requires a native linux/amd64 image builder")
			}

			consumer := livetest.OpenConsumer(t, target, root)

			const workflow = "development.yml"

			workflows := consumer.Workflows(t)
			if len(workflows) != 1 || workflows[workflow] == "" {
				t.Fatal("the consumer must supply only .forgejo/workflows/development.yml")
			}

			layer, repo := newConsumerRepos(t, target, consumer, "container-development", visibility.Private)

			registry, err := livetest.RegistryHost(target)
			if err != nil {
				t.Fatal(err)
			}

			image := registry + "/" + strings.ToLower(livetest.RepoSlug(target, repo))

			plan, err := layer.PlanConsumer(workflows, livetest.ConsumerFacts{
				Inputs: map[string]string{"forge-ci-repository": layer.Repository},
			})
			if err != nil {
				t.Fatal(err)
			}

			main := consumer.Push(t, repo, plan)
			requireObjectFormats(t, consumer.ObjectFormat, main, layer.Commit)

			if err = retry.Run(livetest.Context(t), nil, 30, 2*time.Second, func() error {
				before, found := livetest.Branches(t, target, repo)["main"]
				if !found {
					return fmt.Errorf("main is not visible after push: %w", errs.ErrDependencyUnavailable)
				}

				if before != main {
					return retry.Permanent(fmt.Errorf("main = %s, want %s: %w", before, main, errs.ErrValidation))
				}

				return nil
			}, retry.WithBackoff(retry.Constant)); err != nil {
				t.Fatal(err)
			}

			var head string

			defer func() {
				if after := livetest.BranchCommitSHA(t, target, repo, "main"); after != main {
					t.Errorf("development advanced main: before=%s after=%s", main, after)
				}

				if livetest.RepoCount(t, target, repo, "/tags") != 0 || len(livetest.Releases(t, target, repo)) != 0 {
					t.Error("development created Git tags or releases")
				}

				for tag := range livetest.RegistrySnapshot(t, target, repo) {
					if tag != "dev-"+head {
						t.Errorf("development published unexpected image tag %q", tag)
					}
				}
			}()

			label := livetest.RunnerLabel(t, target)
			livetest.EnableActions(t, target, repo)

			for name, value := range plan.Variables {
				livetest.SetRepoVariable(t, target, repo, name, value)
			}

			livetest.SetRepoVariable(t, target, repo, layerRunnerVariable, runnerLabelsJSON(t, label))
			livetest.SetRepoVariable(t, target, repo, containerRunnerVariable, runnerLabelsJSON(t, privileged))
			livetest.SetRepoVariable(t, target, repo, "FORGE_CI_REPOSITORY", layer.Repository)
			// The sole build/publish job consumes the account's package credential.
			livetest.SetRepoVariable(t, target, repo, "DEVELOPMENT_REGISTRY_USERNAME", target.CredentialUsername)
			livetest.SetRepoSecret(t, target, repo, "DEVELOPMENT_REGISTRY_TOKEN", target.Token)
			head = consumer.PushChange(t, repo, "development", main)
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

			// The one job builds the image, so it runs on the container runner.
			assertLayerJobs(t, jobs, run.ID, privileged, "Check: development container")

			var source struct {
				Branch string `json:"prettyref"`
				SHA    string `json:"commit_sha"`
			}

			url := fmt.Sprintf("%s/api/v1/repos/%s/actions/runs/%d", target.BaseURL(), livetest.RepoSlug(target, repo), run.ID)
			if err = json.Unmarshal(livetest.Download(t, target, url, 1<<20), &source); err != nil {
				t.Fatal(err)
			}

			if source.Branch != "development" || source.SHA != head || livetest.BranchCommitSHA(t, target, repo, "development") != head {
				t.Fatalf("run source = %+v, want development at %s", source, head)
			}

			artifacts := livetest.RunArtifacts(t, target, repo, run.ID)
			if len(artifacts) != 1 || artifacts[0].Name != "fc-cargo-dev-"+head || artifacts[0].Expired || artifacts[0].SizeInBytes <= 0 || artifacts[0].SizeInBytes > 32<<10 {
				t.Fatalf("want one bounded exact-source development artifact, got %+v", artifacts)
			}

			tree, err := livetest.ZIPTree(livetest.ArtifactZIP(t, target, repo, artifacts[0].ID), 4096)
			if err != nil {
				t.Fatal(err)
			}

			digest, found := livetest.ImageDigest(t, target, repo, "dev-"+head)
			if !found || !container.ValidDigest(digest) {
				t.Fatalf("registry did not serve a development digest: %q", digest)
			}

			work := t.TempDir()
			auth := livetest.RegistryAuthFile(t, target, work)

			readback := livetest.CLIIn(t, target, repo, livetest.RunOptions{Dir: work},
				"container", "image", "usable-digest", "--ref", image+"@"+digest,
				"--arch", "amd64", "--auth-file", auth,
				"--require-label", "org.opencontainers.image.revision="+head)
			if readback.ExitCode != 0 || strings.TrimSpace(readback.Stdout) != digest {
				t.Fatalf("development image does not match its source: exit=%d\n%s", readback.ExitCode, readback.Stderr)
			}

			want := map[string]string{
				"commit.txt": head, "image-digest.txt": digest,
				"image-ref.txt": image + "@" + digest, "image-tag.txt": image + ":dev-" + head,
			}
			if len(tree) != len(want) {
				t.Fatalf("want four artifact files, got %v", sortedNames(tree))
			}

			for name, value := range want {
				if string(tree[name]) != value+"\n" {
					t.Errorf("artifact %s = %q, want %q", name, tree[name], value+"\n")
				}
			}
		})
	}
}
