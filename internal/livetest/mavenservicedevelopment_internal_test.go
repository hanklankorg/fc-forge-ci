// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"gopkg.in/yaml.v3"
)

// Exercise variations entirely with local Git objects: no Java, Maven, API or
// container execution. The admitted objects and every non-CI blob must survive.
func TestMavenServiceDevelopmentPreservesSource(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root, git := stageTestRepo(t, format)
			for path, body := range map[string]string{
				"pom.xml":                "<project><artifactId>different-service</artifactId><version>9.2.0</version></project>",
				"Containerfile":          "FROM example.invalid/java\nCOPY original.jar /app.jar\n",
				"src/main/App.java":      "// original business code\n",
				"development/checks.xml": "<rules/>",
			} {
				stageTestWrite(t, root, path, body, 0644)
			}
			stageTestCommit(t, git, root)
			source, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			admitted := source.Commit
			recipe, err := filepath.Abs("../../test/live/demos/hello-maven-service/consumer")
			if err != nil {
				t.Fatal(err)
			}
			c := MavenServiceDevelopmentConfig{RecipeDirectory: recipe, JavaImage: "registry.invalid/java:25@sha256:" + strings.Repeat("a", 64), Branch: "feature/other-service", ReadinessURL: "http://127.0.0.1:9090/ready"}
			fixture, err := mavenServiceDevelopmentFixture(t.Context(), source, c)
			if err != nil {
				t.Fatal(err)
			}
			diff, err := git.run(t.Context(), fixture.stage, "diff-tree", "--no-commit-id", "--name-only", "-r", admitted, fixture.Commit)
			if err != nil {
				t.Fatal(err)
			}
			for path := range strings.SplitSeq(diff, "\n") {
				if !strings.HasPrefix(path, ".forge-ci/") && path != ".forgejo/workflows/development.yml" {
					t.Fatalf("changed source blob: %s", path)
				}
			}
			if err = git.requireClean(t.Context(), root, "."); err != nil {
				t.Fatal(err)
			}
			workflow, err := githubWorkflow(t.Context(), fixture, ".forgejo/workflows/development.yml")
			if err != nil {
				t.Fatal(err)
			}
			var parsed map[string]any
			if err = yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
				t.Fatalf("invalid rendered workflow: %v", err)
			}
			var shellJobs struct {
				Jobs map[string]struct {
					Steps []struct{ Run string }
				}
			}
			if err = yaml.Unmarshal([]byte(workflow), &shellJobs); err != nil {
				t.Fatal(err)
			}
			for name, job := range shellJobs.Jobs {
				for _, step := range job.Steps {
					command := exec.CommandContext(t.Context(), "bash", "-n")
					command.Stdin = strings.NewReader(step.Run)
					if out, err := command.CombinedOutput(); err != nil {
						t.Fatalf("invalid %s shell: %v: %s", name, err, out)
					}
				}
			}
			decl, err := githubWorkflow(t.Context(), fixture, ".forge-ci/service-development.yml")
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Parse([]byte(decl))
			if err != nil {
				t.Fatal(err)
			}
			if err = config.Validate(cfg); err != nil {
				t.Fatal(err)
			}
			if err = config.Derive(cfg); err != nil {
				t.Fatal(err)
			}
			tag, err := version.ComposeSnapshotContainerTag("9.2.0", c.Branch, fixture.Commit[:7])
			if err != nil {
				t.Fatal(err)
			}
			plan, err := snapshotimage.New(pipeline.NewConfigPlan(cfg), snapshotimage.Input{SourceSHA: fixture.Commit, SourceBranch: c.Branch, Tag: tag, ImageRepository: "registry.invalid/owner/service", Publish: true})
			if err != nil || len(plan.Artifacts) != 0 || len(plan.Builds) != 1 || plan.Builds[0].ContainerFile != "Containerfile" {
				t.Fatalf("invalid container-first declaration or unwanted duplicate producer: %+v %v", plan, err)
			}
			for _, alter := range []func(*MavenServiceDevelopmentConfig){
				func(c *MavenServiceDevelopmentConfig) { c.Branch = "main" },
				func(c *MavenServiceDevelopmentConfig) { c.ReadinessURL = "http://external.invalid:9090/ready" },
				func(c *MavenServiceDevelopmentConfig) { c.JavaImage = "maven:latest" },
			} {
				bad := c
				alter(&bad)
				if _, err = mavenServiceDevelopmentFixture(t.Context(), source, bad); err == nil {
					t.Fatal("accepted invalid trial infrastructure")
				}
			}
		})
	}
}
