// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Canonical delivery builds Linux files in a Linux builder image, with Linux
// tool profiles, so it serves no darwin job. Every job on a macOS runner takes
// the one documented path instead: it checks the engine out at the resolved
// full commit and builds forge-ci from that checkout with the engine's own
// action, before any forge-ci command, and installs nothing else.
func TestMacOSLanesBuildTheEngineFromItsApprovedCheckout(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join(reporoot.Path(t), ".github", "workflows", "*.yml"))
	require.NoError(t, err)

	lanes := 0

	for _, path := range paths {
		var workflow struct {
			On struct {
				Call struct {
					Inputs map[string]struct {
						Required bool `yaml:"required"`
					} `yaml:"inputs"`
				} `yaml:"workflow_call"`
			} `yaml:"on"`
			Jobs map[string]struct {
				RunsOn string `yaml:"runs-on"`
				Steps  []struct {
					Name string            `yaml:"name"`
					Uses string            `yaml:"uses"`
					Run  string            `yaml:"run"`
					With map[string]string `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/"+filepath.Base(path)), &workflow), path)

		for jobName, job := range workflow.Jobs {
			if !strings.Contains(job.RunsOn, "runs-on-darwin-arm64") && !strings.HasPrefix(job.RunsOn, "macos-") {
				continue
			}

			lanes++

			require.True(t, workflow.On.Call.Inputs["forge-ci-binary-ref"].Required, "%s: a macOS lane takes the full engine commit", path)

			built := false

			for index, step := range job.Steps {
				require.NotContains(t, step.Run, "install-forge-ci", "%s/%s", path, jobName)
				require.NotContains(t, step.Uses, "install-forge-ci", "%s/%s", path, jobName)

				if strings.Contains(step.Run, "forge-ci ") {
					require.True(t, built, "%s/%s runs forge-ci before building it from the approved engine", path, jobName)
				}

				if step.Uses != "./_forge_ci_engine/.github/actions/build-darwin-cli" {
					continue
				}

				require.Equal(t, approvedEngineCommit, step.With["engine-commit"], "%s/%s", path, jobName)
				require.Positive(t, index, "%s/%s", path, jobName)

				checkout := job.Steps[index-1]
				require.True(t, strings.HasPrefix(checkout.Uses, "actions/checkout@"), "%s/%s", path, jobName)
				require.Equal(t, "_forge_ci_engine", checkout.With["path"])
				require.Equal(t, resolvedForgeCIRepository, checkout.With["repository"])
				require.Equal(t, approvedEngineCommit, checkout.With["ref"])
				require.Equal(t, "false", checkout.With["persist-credentials"])

				built = true
			}

			require.True(t, built, "%s/%s builds no engine", path, jobName)
		}
	}

	require.Equal(t, 5, lanes, "the macOS lanes are lint-swift's two jobs, build-xcode-ios's unsigned and signed jobs, and publish-apple-appstore")

	var action struct {
		Inputs map[string]struct {
			Required bool `yaml:"required"`
		} `yaml:"inputs"`
		Runs struct {
			Steps []struct {
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/actions/build-darwin-cli/action.yml"), &action))
	require.True(t, action.Inputs["engine-commit"].Required)
	require.Len(t, action.Runs.Steps, 3)

	gate, toolchain, build := action.Runs.Steps[0], action.Runs.Steps[1], action.Runs.Steps[2]
	require.Contains(t, gate.Run, `"$RUNNER_OS" != macOS`)
	require.Contains(t, gate.Run, `=~ ^[0-9a-f]{40}$`)
	require.Contains(t, gate.Run, `git -C "$ENGINE_CHECKOUT" rev-parse HEAD`)
	require.Equal(t, "actions/setup-go@4a3601121dd01d1626a1e23e37211e3254c1c06c", toolchain.Uses)
	require.Equal(t, "${{ github.action_path }}/../../../go.mod", toolchain.With["go-version-file"])
	require.Equal(t, "false", toolchain.With["cache"])
	require.Equal(t, "0", build.Env["CGO_ENABLED"])
	require.Equal(t, "local", build.Env["GOTOOLCHAIN"])
	require.Contains(t, build.Run, `go build -trimpath -buildvcs=false`)
	require.Contains(t, build.Run, `./cmd/forge-ci`)
	require.Contains(t, build.Run, `>>"$GITHUB_PATH"`)
}

// approvedEngineCommit is the raw forge-ci-binary-ref input, which the
// resolver admits only as a full commit before a macOS job starts.
const approvedEngineCommit = "${{ inputs['forge-ci-binary-ref'] }}"
