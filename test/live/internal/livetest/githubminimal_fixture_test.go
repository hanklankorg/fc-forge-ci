// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const minimalNodeImage = "ghcr.io/itiquette/forge-ci-runtime-node-24@sha256:e90d3dcfdbe048329e0eb23cad813b95722fcb077b2261038da34bbca91e6d91"
const minimalBotEmail = "forge-lab-minimal@hosted.invalid"

func minimalPreparationWorkflow(t *testing.T, engine, commit string, multi bool) string {
	t.Helper()

	leaf, with := "version-bump.yml", "      project-type: npm\n"
	if multi {
		leaf = "release-prepare-stage.yml"
		plan := pipeline.ReleasePrepareStagePlan{Version: pipeline.ReleasePlanVersion, Stage: "prepare", Targets: pipeline.ReleasePrepareTargets{
			VersionBump: pipeline.TargetPlan[pipeline.PlannedArtifact]{Runs: true, Items: []pipeline.PlannedArtifact{
				{Name: "native", ProjectType: projecttype.NPM, WorkingDirectory: "."},
				{Name: "text", ProjectType: projecttype.Gradle, WorkingDirectory: "second"},
			}}}}

		planJSON, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}

		with = "      authorized-source-sha: ${{ github.sha }}\n      prepare-stage-plan-json: '" + string(planJSON) + "'\n"
	}

	// The scratch engine has no stable release. Bootstrap from the same immutable
	// snapshot as the called workflows, including their checksum-pinned CLI install.
	with += "      forge-ci-binary-ref: " + commit + "\n"

	workflow := `name: Minimal version preparation
on:
  push:
    tags: ['release-request/v*']
permissions:
  contents: read
jobs:
  prepare:
    uses: ENGINE/.github/workflows/LEAF@COMMIT
    permissions:
      contents: write
      actions: read
    with:
      branch: main
      runtime-image: IMAGE
      git-signing-method: ssh
      bot-name: Forge Lab Minimal
      bot-email: EMAIL
WITH    secrets:
      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
      RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}
`

	return strings.NewReplacer("ENGINE", engine, "LEAF", leaf, "COMMIT", commit, "IMAGE", minimalNodeImage, "EMAIL", minimalBotEmail, "WITH", with).Replace(workflow)
}

func TestMinimalPreparationWorkflowPinsBootstrapToEngineSnapshot(t *testing.T) {
	t.Parallel()

	const engine = "fixture-owner/minimal-engine"

	commit := strings.Repeat("a", 40)

	for _, tc := range []struct {
		name, leaf string
		multi      bool
	}{
		{"standalone", "version-bump.yml", false},
		{"multi-artifact", "release-prepare-stage.yml", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var workflow struct {
				Jobs map[string]struct {
					Uses string            `yaml:"uses"`
					With map[string]string `yaml:"with"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal([]byte(minimalPreparationWorkflow(t, engine, commit, tc.multi)), &workflow); err != nil {
				t.Fatal(err)
			}

			call := workflow.Jobs["prepare"]
			if call.Uses != engine+"/.github/workflows/"+tc.leaf+"@"+commit {
				t.Fatalf("consumer must call the immutable fixture engine snapshot: %s", call.Uses)
			}

			if ref := call.With["forge-ci-binary-ref"]; ref != commit {
				t.Errorf("bootstrap ref = %q, want fixture engine commit %s; an empty ref selects a stable release absent from the scratch engine", ref, commit)
			}

			if call.With["runtime-image"] != minimalNodeImage {
				t.Error("fixture toolchain image must remain digest-pinned")
			}
		})
	}
}

var minimalPreparationWorkflows = []string{
	"version-bump.yml",
	"release-prepare-stage.yml",
	"generate-changelog.yml",
	"resolve-forge-ci-bootstrap.yml",
}

func TestMinimalPreparationSnapshotIncludesLocalWorkflowDependencies(t *testing.T) {
	t.Parallel()

	for _, name := range minimalPreparationWorkflows {
		body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github/workflows", name))
		if err != nil {
			t.Fatal(err)
		}

		var workflow struct {
			Jobs map[string]struct {
				Uses string `yaml:"uses"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(body, &workflow); err != nil {
			t.Fatal(err)
		}

		for job, call := range workflow.Jobs {
			dependency, local := strings.CutPrefix(call.Uses, "./.github/workflows/")
			if local && !slices.Contains(minimalPreparationWorkflows, dependency) {
				t.Errorf("%s job %s calls %s, missing from the hosted snapshot", name, job, dependency)
			}
		}
	}
}
