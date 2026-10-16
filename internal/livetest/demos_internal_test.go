// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

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
