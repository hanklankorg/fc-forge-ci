// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"strings"
	"testing"
)

func TestNPMBuildSelectsConfiguredNodeAndRecordsTheExecutedVersion(t *testing.T) {
	t.Parallel()
	var workflow struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	loadYAML(t, ".github/workflows/build-npm.yml", &workflow)
	selected, recorded, built := -1, -1, -1
	for index, candidate := range workflow.Jobs["build-npm"].Steps {
		if strings.HasPrefix(candidate.Uses, "actions/setup-node@") {
			selected = index
			if candidate.With["node-version"] != "${{ inputs['node-version'] }}" || candidate.If != "${{ inputs['node-version'] != '' }}" {
				t.Fatal("Node selection must use the optional input")
			}
		}
		if candidate.ID == "node-version" {
			recorded = index
		}
		if strings.Contains(candidate.Run, "forge-ci build npm run") {
			built = index
			if candidate.Env["NODE_VERSION"] != "${{ steps['node-version'].outputs.version }}" {
				t.Fatal("summary must describe the executed Node, not a hardcoded major")
			}
		}
	}
	if selected < 0 || recorded <= selected || built <= recorded {
		t.Fatalf("toolchain selection/record/build order: %d %d %d", selected, recorded, built)
	}
	record := jobStep(t, ".github/workflows/build-npm.yml", "build-npm", "Record executed Node version")
	box, env := engineSandbox(t, "node-version", map[string]string{"node": "#!/bin/sh\nprintf 'v26.11.1\\n'\n"})
	result := box.run(record.Run, append(env, "GITHUB_OUTPUT="+box.outputs)...)
	if result.status != 0 || box.output() != "version=v26.11.1\n" {
		t.Fatalf("executed Node was not recorded: status=%d outputs=%q", result.status, box.output())
	}
	box.write("tools/node", "#!/bin/sh\nexit 127\n", 0o700)
	failed := box.run(record.Run, append(env, "GITHUB_OUTPUT="+box.outputs)...)
	if failed.status == 0 || box.output() != "" {
		t.Fatalf("failed Node execution produced a successful/empty version handoff: status=%d outputs=%q", failed.status, box.output())
	}
	for _, path := range []string{".github/workflows/release-build-stage.yml", ".github/workflows/release-snapshot-build-stage.yml"} {
		var stage struct {
			Jobs map[string]struct {
				With map[string]any `yaml:"with"`
			} `yaml:"jobs"`
		}
		loadYAML(t, path, &stage)
		if stage.Jobs["npm"].With["node-version"] != "${{ matrix.artifact.npm.node_version || '' }}" {
			t.Fatalf("%s drops the selected npm Node version", path)
		}
	}
}
