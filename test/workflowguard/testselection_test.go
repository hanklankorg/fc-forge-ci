// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

func TestReleaseTestSelectionsComeOnlyFromAdministratorInputs(t *testing.T) {
	root := reporoot.Path(t)

	type step struct {
		Env map[string]string `yaml:"env"`
		Run string            `yaml:"run"`
	}

	type job struct {
		Uses    string            `yaml:"uses"`
		With    map[string]string `yaml:"with"`
		Steps   []step            `yaml:"steps"`
		Needs   []string          `yaml:"needs"`
		Outputs map[string]string `yaml:"outputs"`
		If      string            `yaml:"if"`
	}

	read := func(name string) map[string]job {
		t.Helper()

		body, err := os.ReadFile(filepath.Join(root, ".github/workflows", name))
		if err != nil {
			t.Fatal(err)
		}

		var workflow struct {
			Jobs map[string]job `yaml:"jobs"`
			On   struct {
				WorkflowCall struct {
					Inputs map[string]struct {
						Default string `yaml:"default"`
					} `yaml:"inputs"`
				} `yaml:"workflow_call"`
			} `yaml:"on"`
		}
		if err := yaml.Unmarshal(body, &workflow); err != nil {
			t.Fatal(err)
		}

		for _, ecosystem := range []string{"go", "cargo"} {
			if input, ok := workflow.On.WorkflowCall.Inputs[ecosystem+"-test-selections-json"]; ok && input.Default != "" {
				t.Fatalf("%s converts omission into an explicit selection map", name)
			}
		}

		return workflow.Jobs
	}
	buildJobs := read("release-build-stage.yml")

	preflight := buildJobs["validate-test-selections"]
	if len(preflight.Steps) != 1 {
		t.Fatal("missing whole-map preflight")
	}

	validation := preflight.Steps[0]
	if validation.Env["BUILD_STAGE_PLAN_JSON"] != "${{ inputs['build-stage-plan-json'] }}" ||
		validation.Env["ADMIN_GO_SELECTIONS_JSON"] != "${{ inputs['go-test-selections-json'] }}" ||
		validation.Env["ADMIN_CARGO_SELECTIONS_JSON"] != "${{ inputs['cargo-test-selections-json'] }}" ||
		!strings.Contains(validation.Run, "forge-ci plan validate-test-selections") ||
		!strings.Contains(validation.Run, `--build-stage-plan-json="$BUILD_STAGE_PLAN_JSON"`) ||
		!strings.Contains(validation.Run, `--go-test-selections-json="$ADMIN_GO_SELECTIONS_JSON"`) ||
		!strings.Contains(validation.Run, `--cargo-test-selections-json="$ADMIN_CARGO_SELECTIONS_JSON"`) {
		t.Fatalf("preflight owner/inputs lost: %+v", validation)
	}

	for _, name := range []string{"maven", "npm", "gradle", "go", "cargo", "gradle-android", "xcode-ios", "summarize-build-stage"} {
		if !slices.Contains(buildJobs[name].Needs, "validate-test-selections") {
			t.Fatalf("%s can bypass whole-map preflight", name)
		}
	}

	if !strings.Contains(buildJobs["summarize-build-stage"].If, "needs.validate-test-selections.result == 'success'") {
		t.Fatal("invalid whole-map preflight can receive a successful stage summary")
	}

	for _, ecosystem := range []string{"go", "cargo"} {
		key := ecosystem + "-test-selections-json"
		found := false

		for _, job := range read("release-orchestrator.yml") {
			if job.Uses == "./.github/workflows/release-build-stage.yml" {
				found = true

				if job.With[key] != "${{ inputs['"+key+"'] }}" {
					t.Fatalf("admin selection not forwarded: %v", job.With)
				}
			}
		}

		if !found {
			t.Fatal("missing build stage")
		}

		stage := buildJobs[ecosystem]

		if preflight.Outputs[key] != "${{ steps.validate.outputs."+key+" }}" {
			t.Fatal("missing validated mapping output")
		}

		want := fmt.Sprintf("${{ needs.validate-test-selections.outputs.%s != '{}' && toJson(fromJson(needs.validate-test-selections.outputs.%s)[matrix.artifact.name]) || '' }}", key, key)
		if stage.With["test-selection-json"] != want {
			t.Fatalf("selection can fall back from an invalid/missing mapped value or consumer plan: %v", stage.With)
		}

		found = false

		for _, job := range read("build-" + ecosystem + ".yml") {
			for _, step := range job.Steps {
				if step.Env["ADMIN_TEST_SELECTION_JSON"] == "" {
					continue
				}

				found = true

				if step.Env["ADMIN_TEST_SELECTION_JSON"] != "${{ inputs['test-selection-json'] }}" ||
					step.Env["ADMIN_APPLICATION_TESTS"] != "${{ inputs['enforce-application-tests'] }}" ||
					step.Run != "forge-ci build "+ecosystem+" run --enforce-application-tests=\"$ADMIN_APPLICATION_TESTS\" --test-selection-json=\"$ADMIN_TEST_SELECTION_JSON\"" {
					t.Fatalf("selection or enforcement lost at native command boundary: %+v", step)
				}
			}
		}

		if !found {
			t.Fatal("missing selected native build step")
		}
	}
}
