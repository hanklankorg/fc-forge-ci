// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later
//go:build live

package conformance_test

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

func TestForgejoMavenLibraryConsumer_Development(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("set FORGE_CI_LIVE_CONSUMER")
	}
	target := livetest.Accept(t, provider.ForgeForgejo)
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner, livetest.NeedsPackages) {
		t.Skip("requires native runner and packages")
	}
	if livetest.RunnerArch(t, target) != "amd64" {
		t.Fatal("requires native amd64")
	}
	recipe, err := filepath.Abs("../../../test/live/demos/hello-maven/consumer")
	if err != nil {
		t.Fatal(err)
	}
	source := livetest.OpenConsumer(t, target, root)
	admitted := source.Commit
	consumer, model := livetest.NewMavenLibraryDevelopmentFixture(t, source, livetest.MavenLibraryFixtureConfig{RecipeDirectory: recipe, JavaImage: os.Getenv("FORGE_CI_LIVE_MAVEN_LIBRARY_IMAGE"), Profile: os.Getenv("FORGE_CI_LIVE_MAVEN_LIBRARY_PROFILE")}, os.Getenv("FORGE_CI_LIVE_MAVEN_LIBRARY_ORACLE"))
	layer, repo := newConsumerRepos(t, target, consumer, "maven-library-development", false)
	name := livetest.MavenPackageName(target, model.Group, model.Artifact)
	if slices.Contains(livetest.PublishedPackageVersions(t, target, repo, "maven", name), model.Version) {
		t.Fatal("snapshot coordinate already exists in disposable owner; refusing collision")
	}
	t.Cleanup(func() { livetest.DeletePublishedPackage(t, target, repo, "maven", name, model.Version) })
	plan, err := layer.PlanConsumer(consumer.Workflows(t), livetest.ConsumerFacts{Inputs: map[string]string{"forge-ci-repository": layer.Repository}})
	if err != nil {
		t.Fatal(err)
	}
	main := consumer.Push(t, repo, plan)
	defer func() {
		if got := livetest.BranchCommitSHA(t, target, repo, "main"); got != main {
			t.Errorf("dev changed main: %s != %s", got, main)
		}
		if livetest.RepoCount(t, target, repo, "/tags") != 0 || len(livetest.Releases(t, target, repo)) != 0 {
			t.Error("dev created stable tags/releases")
		}
	}()
	livetest.EnableActions(t, target, repo)
	for key, value := range plan.Variables {
		livetest.SetRepoVariable(t, target, repo, key, value)
	}
	livetest.SetRepoVariable(t, target, repo, layerRunnerVariable, runnerLabelsJSON(t, livetest.RunnerLabel(t, target)))
	livetest.SetRepoVariable(t, target, repo, "FORGE_CI_REPOSITORY", layer.Repository)
	livetest.SetRepoSecret(t, target, repo, "DEVELOPMENT_REGISTRY_TOKEN", target.Token)
	head := consumer.PushChange(t, repo, "development", main)
	run := livetest.WaitForConsumerRun(t, target, repo, "development.yml", "push", "")
	jobs := livetest.RunJobs(t, target, repo, run.ID)
	scenario := layerScenario{target: target, repo: repo, layer: layer, label: livetest.RunnerLabel(t, target)}
	scenario.logJobs(t, jobs)
	if run.Status != jobSuccess {
		t.Fatalf("development run %d: %s", run.ID, run.Status)
	}
	oracle := false
	for _, job := range jobs {
		if log, served := livetest.JobLogIfServed(t, target, repo, job); served && strings.Contains(log, "LIBRARY_REGISTRY_ORACLE_OK:"+head) {
			oracle = true
		}
	}
	if !oracle {
		t.Fatal("runner did not confirm fresh-registry oracle for exact dev SHA")
	}
	base := strings.ReplaceAll(model.Group, ".", "/") + "/" + model.Artifact + "/" + model.Version + "/"
	var metadata struct {
		Versions []struct {
			Extension  string `xml:"extension"`
			Classifier string `xml:"classifier"`
			Value      string `xml:"value"`
		} `xml:"versioning>snapshotVersions>snapshotVersion"`
	}
	if err := xml.Unmarshal(livetest.MavenRegistryFile(t, target, repo, base+"maven-metadata.xml"), &metadata); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{{"pom", ""}, {"jar", ""}, {"jar", "sources"}, {"jar", "javadoc"}} {
		found := false
		for _, entry := range metadata.Versions {
			if entry.Extension == want[0] && entry.Classifier == want[1] && !strings.Contains(entry.Value, "SNAPSHOT") {
				suffix := ""
				if entry.Classifier != "" {
					suffix = "-" + entry.Classifier
				}
				if len(livetest.MavenRegistryFile(t, target, repo, base+model.Artifact+"-"+entry.Value+suffix+"."+entry.Extension)) == 0 {
					t.Fatal("empty timestamped snapshot")
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("missing timestamped snapshot metadata for %v", want)
		}
	}
	t.Logf("admitted source %s; exact development source %s; engine %s; native build, scratch snapshot registry, fresh Maven classifiers and runner oracle passed", admitted, head, layer.Commit)
}
