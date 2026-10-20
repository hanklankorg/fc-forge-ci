// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/gitlaboutput"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestGitLabImageDemosConsumeActualDotenvOutputs(t *testing.T) {
	t.Parallel()
	for _, demo := range []string{"hello-maven-service", "hello-cargo-container", "hello-npm-service"} {
		t.Run(demo, func(t *testing.T) {
			t.Parallel()
			body, err := os.ReadFile(filepath.Join(reporoot.Path(t), "test", "live", "demos", demo, "consumer", ".gitlab", "build-release.sh"))
			if err != nil {
				t.Fatal(err)
			}
			_, fragment, found := strings.Cut(string(body), "image='' digest=''")
			if !found {
				t.Fatal("demo has no image-output consumer")
			}
			fragment, _, found = strings.Cut(fragment, "--default-sbom")
			if !found {
				t.Fatal("demo has no image-ledger hand-off")
			}
			fragment = "image='' digest=''" + fragment + "--default-sbom\n"
			dir := t.TempDir()
			image := "registry.example/trial/service"
			digest := "sha256:" + strings.Repeat("a", 64)
			sink := gitlaboutput.New(filepath.Join(dir, "image.env"))
			if err := sink.Set(t.Context(), "image", image); err != nil {
				t.Fatal(err)
			}
			if err := sink.Set(t.Context(), "digest", digest); err != nil {
				t.Fatal(err)
			}
			if err := sink.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(dir, "ledger-args")
			command := exec.CommandContext(t.Context(), "bash", "-c", "set -euo pipefail\nforge-ci() { printf '%s\\n' \"$@\" > \"$CAPTURE\"; }\n"+fragment)
			command.Env = []string{"PATH=/usr/bin:/bin", "auth_dir=" + dir, "CAPTURE=" + capture, "RELEASE_TAG=v1.2.3"}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("demo rejected real GitLab output: %v\n%s", err, output)
			}
			args, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Join([]string{"container", "ledger", "add", "--role", "runtime", "--image-name", image, "--digest", digest,
				"--final-tag-name", "v1.2.3", "--tag", "v1.2.3", "--default-sbom"}, "\n") + "\n"
			if string(args) != want {
				t.Fatalf("ledger arguments=%q; want %q", args, want)
			}
		})
	}
}

// Every demo in test/live/demos renders for each forge it has workflows for,
// the way `just demo` would render it, so a demo that drifted from what the
// live tier accepts fails here and not in front of an audience.
func TestDemosRenderForEveryForgeTheyServe(t *testing.T) {
	t.Parallel()

	root := filepath.Join(reporoot.Path(t), "test", "live", "demos")

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
		t.Fatal("no demo in test/live/demos")
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

	if gate, err := os.ReadFile(filepath.Join(consumer, GitLabGateFile)); err == nil { //nolint:gosec // repository fixture.
		forges++

		layer := Layer{ServerURL: "https://gitlab.example", Repository: "forgeci/fci-engine", Commit: strings.Repeat("b", 40)}
		images := GitLabGateImages{Job: "registry.example/job@sha256:" + strings.Repeat("c", 64), Lint: "registry.example/lint@sha256:" + strings.Repeat("d", 64)}

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

		body, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // repository fixture.
		if err != nil {
			t.Fatal(err)
		}

		files[entry.Name()] = string(body)
	}

	return files
}
