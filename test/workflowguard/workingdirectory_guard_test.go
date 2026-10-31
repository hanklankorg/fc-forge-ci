// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// A step that runs inside a project directory and also hands that directory
// to the engine applies it twice: forge-ci build npm run, started in
// packages/frontend with WORKING_DIRECTORY=packages/frontend, looked for
// packages/frontend/packages/frontend/package.json, so every npm or Cargo
// project below the repository root failed on GitHub. The engine resolves the
// directory itself; the step runs from the workspace root.
func TestNoStepAppliesItsWorkingDirectoryTwice(t *testing.T) {
	t.Parallel()

	for _, sub := range []string{filepath.Join(".github", "workflows"), filepath.Join(".forgejo", "workflows")} {
		dir := filepath.Join(reporoot.Path(t), sub)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)

		for _, entry := range entries {
			if filepath.Ext(entry.Name()) != ".yml" {
				continue
			}

			body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			require.NoError(t, err)

			var workflow struct {
				Jobs map[string]struct {
					Defaults struct {
						Run struct {
							WorkingDirectory string `yaml:"working-directory"`
						} `yaml:"run"`
					} `yaml:"defaults"`
					Steps []struct {
						Name             string            `yaml:"name"`
						Run              string            `yaml:"run"`
						WorkingDirectory string            `yaml:"working-directory"`
						Env              map[string]string `yaml:"env"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			require.NoError(t, yaml.Unmarshal(body, &workflow), entry.Name())

			for jobName, job := range workflow.Jobs {
				for _, step := range job.Steps {
					where := step.WorkingDirectory
					if where == "" && step.Run != "" {
						where = job.Defaults.Run.WorkingDirectory
					}

					if strings.TrimSpace(where) == "" || where == "." {
						continue
					}

					for name, value := range step.Env {
						if strings.HasSuffix(name, "WORKING_DIRECTORY") && strings.TrimSpace(value) == strings.TrimSpace(where) {
							t.Errorf("%s job %s step %q runs in %s and passes it again as %s", filepath.Join(sub, entry.Name()), jobName, step.Name, where, name)
						}
					}
				}
			}
		}
	}
}
