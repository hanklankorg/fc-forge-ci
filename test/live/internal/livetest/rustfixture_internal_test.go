// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestRustFixtureUsesExistingDevelopmentRunners(t *testing.T) {
	t.Parallel()

	const root = "test/live/fixtures/development/rust-development/consumer/"

	workflow := string(reporoot.ReadFile(t, root+".forgejo/workflows/development.yml"))
	layer := sampleLayer()
	layer.admin = testWorkflowAdmin(t)
	layer.Commit = layer.admin.commit

	plan, err := layer.PlanConsumer(map[string]string{"development.yml": workflow}, ConsumerFacts{
		Inputs: map[string]string{"forge-ci-repository": layer.Repository},
	})
	if err != nil {
		t.Fatal(err)
	}

	rebound := plan.Render(workflow)
	if !strings.Contains(rebound, layer.Commit) || strings.Contains(rebound, "forge.example/engine/forge-ci") {
		t.Fatalf("workflow did not bind the selected engine: %s", rebound)
	}

	if err := ValidateKanikoDevelopmentJob(workflow, "Check: Rust development", "CI_RUST_BUILD_IMAGE"); err != nil {
		t.Fatal(err)
	}

	gate := reporoot.ReadFile(t, root+".gitlab/merge-request.yml")
	pipeline := reporoot.ReadFile(t, root+".gitlab-ci.yml")
	image := "registry.example/fixture/job@sha256:" + strings.Repeat("b", 64)

	_, rendered, err := renderGitLabDevelopmentPolicy(pipeline, gate, layer, GitLabGateImages{
		Job: image, JobTags: map[string][]string{"Check: Rust development": {"native-amd64"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, required := range []string{image, layer.Commit, "Check: Rust development", "ci/produce.sh", "development-artifacts/"} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("rendered GitLab gate lacks %q", required)
		}
	}
}
