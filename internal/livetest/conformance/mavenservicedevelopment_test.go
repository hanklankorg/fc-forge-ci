// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

func runForgejoMavenServiceDevelopment(t *testing.T) {
	t.Helper()
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Fatal("set FORGE_CI_LIVE_CONSUMER to the clean selected source checkout")
	}
	if livetest.DemoName() != "" {
		t.Fatal("service development requires disposable scratch repositories")
	}
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		if visibility.Private {
			t.Fatal("service development acceptance currently admits public repositories only")
		}
	}
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner, livetest.NeedsPackages) {
		livetest.NotCoveredf(t, "service development needs a native runner and packages")
	}
	target := livetest.Accept(t, provider.ForgeForgejo)
	if livetest.RunnerArch(t, target) != "amd64" {
		t.Fatal("service development requires native linux/amd64")
	}
	privileged := privilegedRunner(t, target)
	recipe, err := filepath.Abs("../../../test/live/demos/hello-maven-service/consumer")
	if err != nil {
		t.Fatal(err)
	}
	c := livetest.MavenServiceDevelopmentConfig{
		RecipeDirectory: recipe, JavaImage: os.Getenv("FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE"),
		Branch: os.Getenv("FORGE_CI_LIVE_SERVICE_BRANCH"), ReadinessURL: os.Getenv("FORGE_CI_LIVE_SERVICE_READINESS_URL"),
		Fixture: os.Getenv("FORGE_CI_LIVE_SERVICE_FIXTURE"),
	}
	source := livetest.OpenConsumer(t, target, root)
	admitted := source.Commit
	consumer := livetest.NewMavenServiceDevelopmentFixture(t, source, c)
	layer, repo := newConsumerRepos(t, target, consumer, "consumer-development", false)
	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}
	image := registry + "/" + strings.ToLower(livetest.RepoSlug(target, repo))
	plan, err := layer.PlanConsumer(consumer.Workflows(t), livetest.ConsumerFacts{Inputs: map[string]string{"forge-ci-repository": layer.Repository}})
	if err != nil {
		t.Fatal(err)
	}
	main := consumer.Push(t, repo, plan)
	head := consumer.PushChange(t, repo, c.Branch, main)
	requireObjectFormats(t, consumer.ObjectFormat, head, layer.Commit)
	scenario := layerScenario{target: target, repo: repo, layer: layer, label: privileged}
	defer func() {
		scenario.requireBranches(t, map[string]string{"main": main, c.Branch: head})
		scenario.requireNone(t, "/tags", "/releases")
	}()
	variables, err := livetest.MavenServiceDevelopmentVariables(plan.Variables, map[string]string{
		layerRunnerVariable:     runnerLabelsJSON(t, livetest.RunnerLabel(t, target)),
		containerRunnerVariable: runnerLabelsJSON(t, privileged), "FORGE_CI_REPOSITORY": layer.Repository,
		"SERVICE_IMAGE_REPOSITORY": image, "DEVELOPMENT_REGISTRY_USERNAME": target.CredentialUsername,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Forgejo's variables API is unavailable while repository Actions are off.
	livetest.EnableActions(t, target, repo)
	for name, value := range variables {
		livetest.SetRepoVariable(t, target, repo, name, value)
	}
	livetest.SetRepoSecret(t, target, repo, "DEVELOPMENT_REGISTRY_TOKEN", target.Token)
	livetest.DispatchConsumerDevelopment(t, target, repo)
	run := livetest.WaitForConsumerRun(t, target, repo, "development.yml", "workflow_dispatch", "")
	jobs := livetest.RunJobs(t, target, repo, run.ID)
	scenario.logJobs(t, jobs)
	if run.Status != jobSuccess || (run.Event != "" && run.Event != "workflow_dispatch") || run.TriggerEvent != "workflow_dispatch" || run.WorkflowID != "development.yml" {
		t.Fatalf("service development run %d: %s; original Containerfile/POM failures are CI integration failures, not permission to alter source", run.ID, run.Status)
	}
	if runs := livetest.WorkflowRuns(t, target, repo); len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("want exactly the policy dispatch, got %+v", runs)
	}
	artifacts := map[string]map[string][]byte{}
	for _, artifact := range livetest.RunArtifacts(t, target, repo, run.ID) {
		if artifact.Name != "service-jars-"+head && artifact.Name != "service-evidence-"+head && artifact.Name != "snapshot-images-plan" {
			continue
		}
		if artifact.Expired || artifact.SizeInBytes <= 0 || artifact.SizeInBytes > 256<<20 {
			t.Fatalf("invalid service artifact: %+v", artifact)
		}
		address := fmt.Sprintf("%s/api/v1/repos/%s/actions/artifacts/%d/zip", target.BaseURL(), livetest.RepoSlug(target, repo), artifact.ID)
		tree, err := livetest.ZIPTree(livetest.Download(t, target, address, 256<<20), 256<<20)
		if err != nil {
			t.Fatal(err)
		}
		artifacts[artifact.Name] = tree
	}
	var frozen snapshotimage.Plan
	if err = json.Unmarshal(artifacts["snapshot-images-plan"]["snapshot-plan.json"], &frozen); err != nil {
		t.Fatal(err)
	}
	if err = frozen.Validate(); err != nil || frozen.SourceSHA != head || frozen.SourceBranch != c.Branch || len(frozen.Artifacts) != 0 || len(frozen.Builds) != 1 || frozen.Builds[0].Arch != "amd64" {
		t.Fatalf("wrong frozen service plan: %+v: %v", frozen, err)
	}
	evidence := artifacts["service-evidence-"+head]
	var receipts []snapshotimage.Receipt
	if err = json.Unmarshal(evidence["container-images.json"], &receipts); err != nil || len(receipts) != 1 {
		t.Fatalf("missing service receipt: %v", err)
	}
	r := receipts[0]
	if r.SourceSHA != head || r.SourceBranch != c.Branch || r.ImageRepository != image || r.Ref != image+"@"+r.Digest || r.Tag != frozen.Tag || string(evidence["image-ref.txt"]) != r.Ref+"\n" || string(evidence["source-sha.txt"]) != head+"\n" || string(evidence["readiness-url.txt"]) != c.ReadinessURL+"\n" || len(evidence["readiness-body.txt"]) == 0 {
		t.Fatalf("readiness evidence does not bind selected source and published image: %+v", r)
	}
	imageConfig := livetest.ImageConfig(t, target, repo, r.Digest)
	if imageConfig.OS != "linux" || imageConfig.Architecture != "amd64" ||
		imageConfig.Config.Labels["org.opencontainers.image.revision"] != head ||
		imageConfig.Config.Labels["org.opencontainers.image.ref.name"] != c.Branch {
		t.Fatal("published platform image does not match the selected architecture, source and branch")
	}
	if digest, found := livetest.ImageDigest(t, target, repo, r.Tag); !found || digest != r.Digest {
		t.Fatalf("snapshot tag does not resolve to receipt digest: %s", digest)
	}
	for tag := range livetest.RegistrySnapshot(t, target, repo) {
		if tag != r.Tag && tag != r.Tag+"-amd64" {
			t.Errorf("unexpected registry alias %s", tag)
		}
	}
	jars := artifacts["service-jars-"+head]
	if string(jars["source-sha.txt"]) != head+"\n" {
		t.Fatal("checked JAR source differs from selected branch")
	}
	count := 0
	for name, body := range jars {
		if !strings.HasSuffix(name, ".jar") {
			continue
		}
		jar, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil || len(jar.File) == 0 {
			t.Fatalf("not an actual nonempty JAR: %s", name)
		}
		entry := fmt.Sprintf("%x  checked-jars/%s\n", sha256.Sum256(body), name)
		if !strings.Contains(string(jars["SHA256SUMS"]), entry) {
			t.Fatalf("checked JAR checksum differs: %s", name)
		}
		count++
	}
	if count == 0 {
		t.Fatal("no actual checked source JAR")
	}
	t.Logf("admitted=%s policy=%s selected=%s branch=%s engine=%s image=%s readiness=%s; checked JARs=%d; Containerfile rebuilt independently, no JAR/image byte-equality claim", admitted, main, head, c.Branch, layer.Commit, r.Ref, c.ReadinessURL, count)
}
