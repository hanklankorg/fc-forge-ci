// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// A release publishes every container a demo declares to one image
// repository, so each declaration with containers must plan as release images
// there: two containers that would publish the same tag are refused by the
// planner, and a shipped demo must never carry that shape. A container that
// declares extract is an extraction image, which release images never are;
// such a demo declares its release images in another file.
func TestDemoContainersPlanAsReleaseImages(t *testing.T) {
	t.Parallel()

	demos, err := filepath.Glob(filepath.Join(reporoot.Path(t), "test", "live", "fixtures", "consumers", "*", "consumer"))
	if err != nil || len(demos) == 0 {
		t.Fatalf("demo consumers: %v", err)
	}

	for _, consumer := range demos {
		t.Run(filepath.Base(filepath.Dir(consumer)), func(t *testing.T) {
			t.Parallel()

			images, declaration, err := GitLabReleaseImages(consumer)
			if err != nil {
				t.Fatal(err)
			}

			if !images {
				t.Skip("declares no containers")
			}

			path := filepath.Join(consumer, cmp.Or(declaration, DefaultArtifactsConfig))

			declared, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			parsed, err := config.Parse(declared)
			if err != nil {
				t.Fatal(err)
			}

			for _, container := range parsed.Containers {
				if container.Extract != nil {
					t.Skip("declares an extraction image")
				}
			}

			_, err = appconfig.ResolveReleaseImagesPlan(appconfig.EmitReleaseImagesPlanInput{
				Config: appconfig.EmitConfigPlanInput{Path: path},
				Images: pipeline.ReleaseImagesInput{
					ImageRepository: "registry.invalid/demo", Repository: "demo/demo", ReleaseTag: "v1.2.3", ReleasePlatforms: "linux/amd64",
				},
			}, output.Annotator{})
			if err != nil {
				t.Fatalf("release images: %v", err)
			}
		})
	}
}

// The GitLab image demos push no image of their own: the release's image jobs
// build the one container their declaration holds, artifacts.yml unless it
// needs release-images.yml (see GitLabReleaseImages), from image-context/
// where the Containerfile needs it.
func TestGitLabImageDemosLeaveTheirImagesToTheReleaseImageJobs(t *testing.T) {
	t.Parallel()

	for demo, context := range map[string]bool{"hello-maven-service": true, "hello-cargo-container": false, "hello-npm-service": true} {
		t.Run(demo, func(t *testing.T) {
			t.Parallel()

			consumer := filepath.Join(reporoot.Path(t), "test", "live", "fixtures", "consumers", demo, "consumer")

			body, err := os.ReadFile(filepath.Join(consumer, ".gitlab", "build-release.sh"))
			if err != nil {
				t.Fatal(err)
			}

			for _, pushed := range []string{"build-push-oci-image", "ledger add", "CI_JOB_TOKEN", "release-images.json"} {
				if strings.Contains(string(body), pushed) {
					t.Errorf("the GitLab build still publishes images: %s", pushed)
				}
			}

			if strings.Contains(string(body), "image-context") != context {
				t.Errorf("image-context in the build is %t, want %t", !context, context)
			}

			images, declaration, err := GitLabReleaseImages(consumer)
			if err != nil || !images {
				t.Fatalf("the GitLab release builds no images: %v", err)
			}

			declared, err := os.ReadFile(filepath.Join(consumer, cmp.Or(declaration, DefaultArtifactsConfig)))
			if err != nil {
				t.Fatal(err)
			}

			parsed, err := config.Parse(declared)
			if err != nil || len(parsed.Containers) != 1 {
				t.Fatalf("release images: %v", err)
			}
		})
	}
}

// Every demo in test/live/fixtures/consumers renders for each forge it has workflows for,
// the way `just demo` would render it, so a demo that drifted from what the
// live tier accepts fails here and not in front of an audience.
func TestDemosRenderForEveryForgeTheyServe(t *testing.T) {
	t.Parallel()

	root := filepath.Join(reporoot.Path(t), "test", "live", "fixtures", "consumers")

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	demos := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		demos++
		consumer := filepath.Join(root, entry.Name(), "consumer")

		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()

			if !validDemoName(entry.Name()) {
				t.Fatalf("%q is not a demo name the live tier accepts", entry.Name())
			}

			if renderDemo(t, consumer) == 0 {
				t.Error("the demo has workflows for no forge")
			}

			// Workflow rendering alone does not catch an obsolete artifacts
			// schema or a misspelled ecosystem option in a runnable demo.
			artifacts, err := os.ReadFile(filepath.Join(consumer, ".forge-ci", "artifacts.yml"))
			if err != nil {
				t.Fatal(err)
			}

			if _, err := config.Parse(artifacts); err != nil {
				t.Fatalf("artifacts configuration: %v", err)
			}
		})
	}

	if demos == 0 {
		t.Fatal("no demo in test/live/fixtures/consumers")
	}
}

// renderDemo renders one demo's consumer for every forge it has workflows
// for, as the trials do, and returns how many forges that is.
func renderDemo(t *testing.T, consumer string) int {
	t.Helper()

	forges := 0

	if workflows := demoFiles(t, filepath.Join(consumer, consumerWorkflows)); len(workflows) > 0 {
		forges++

		if _, err := sampleLayer().PlanConsumer(workflows, releaseFacts()); err != nil {
			t.Errorf("forgejo: %v", err)
		}
	}

	if gate, err := os.ReadFile(filepath.Join(consumer, GitLabGateFile)); err == nil {
		forges++

		layer := Layer{ServerURL: "https://gitlab.example", Repository: "forgeci/fci-engine", Commit: strings.Repeat("b", 40)}
		images := GitLabGateImages{Job: "registry.example/job@sha256:" + strings.Repeat("c", 64), Lint: "registry.example/lint@sha256:" + strings.Repeat("d", 64)}

		// Each further job image the gate names, as a trial gives them.
		names, err := gitlabGateExtraJobImages(gate)
		if err != nil {
			t.Errorf("gitlab: %v", err)
		}

		images.ExtraJobs = map[string]string{}
		for _, name := range names {
			images.ExtraJobs[name] = "registry.example/" + strings.ToLower(name) + "@sha256:" + strings.Repeat("e", 64)
		}

		if _, err := renderGitLabConsumer(gate, layer, images); err != nil {
			t.Errorf("gitlab: %v", err)
		}
	}

	if workflows := demoFiles(t, filepath.Join(consumer, "test", "live", "github", "workflows")); len(workflows) > 0 {
		forges++

		for name, text := range workflows {
			if _, _, err := RenderGitHubConsumer(name, text, trialEngine, nil); err != nil {
				t.Errorf("github %s: %v", name, err)
			}
		}
	}

	return forges
}

// demoFiles reads the YAML files of one directory by name; a missing
// directory is a forge the demo does not serve.
func demoFiles(t *testing.T, dir string) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		t.Fatal(err)
	}

	files := map[string]string{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}

		files[entry.Name()] = string(body)
	}

	return files
}
