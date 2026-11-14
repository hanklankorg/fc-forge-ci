// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Execute only the plan step's shell against an inert function, not the CLI
// or a provider. Every declared runner input must reach admission unchanged,
// through env (not workflow expression interpolation into shell source).
func TestGitHubPlansPassEveryDeclaredRunnerInput(t *testing.T) {
	t.Parallel()

	known, err := runners.Names(provider.ForgeGitHub)
	require.NoError(t, err)

	for _, tc := range []struct{ file, job, command string }{
		{"release-orchestrator.yml", "parse-config", "plan release"},
		{"release-snapshot-orchestrator.yml", "setup-snapshot", "plan snapshot-release"},
		{"snapshot-images.yml", "plan", "container snapshot-images plan"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			t.Parallel()

			path := ".github/workflows/" + tc.file

			var workflow struct {
				Jobs map[string]struct {
					Steps []struct {
						ID  string            `yaml:"id"`
						Run string            `yaml:"run"`
						Env map[string]string `yaml:"env"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, path), &workflow))

			found := 0

			for _, step := range workflow.Jobs[tc.job].Steps {
				if step.ID != "plan" {
					continue
				}

				found++

				require.NotContains(t, step.Run, "${{")
				cmd := exec.CommandContext(t.Context(), "/bin/bash", "-eu", "-c", "forge-ci() { printf '%s\\0' \"$@\"; };\n"+step.Run)
				// The step's other bindings are inert data here; a later
				// value of the same name wins.
				for key := range step.Env {
					cmd.Env = append(cmd.Env, key+"=inert")
				}

				cmd.Env = append(cmd.Env, "RELEASE_POLICY=auto", "RUNNER_TEMP="+t.TempDir(), "GITHUB_REPOSITORY=team/app")

				want := map[string]bool{}

				for _, name := range declaredInputs(t, path) {
					if !isRunnerInput(name) {
						continue
					}

					require.Contains(t, known, name)

					envName := ""

					for key, value := range step.Env {
						if value == "${{ inputs['"+name+"'] }}" {
							require.Empty(t, envName, "duplicate input binding for %s", name)
							envName = key
						}
					}

					require.NotEmpty(t, envName, "no plan env binding for %s", name)
					// Commas, whitespace and shell metacharacters remain data.
					value := `["self-hosted", "` + name + `; $(false)"]`
					cmd.Env = append(cmd.Env, envName+"="+value)
					want[name+"="+value] = true
				}

				require.NotEmpty(t, want)

				out, err := cmd.Output()
				require.NoError(t, err)

				args := strings.Split(string(bytes.TrimSuffix(out, []byte{0})), "\x00")
				command := strings.Fields(tc.command)
				require.Equal(t, command, args[:len(command)])

				for i := len(command); i < len(args); i++ {
					if args[i] != "--runner-input" {
						continue
					}

					i++
					require.Less(t, i, len(args))
					require.True(t, want[args[i]], "unexpected or duplicate runner input %s", args[i])
					delete(want, args[i])
				}

				require.Empty(t, want, "runner inputs omitted from admission")
			}

			require.Equal(t, 1, found, "expected exactly one plan step")
		})
	}
}
