// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// guardStep is a workflow or composite-action step as the guard reads it.
type guardStep struct {
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	WorkingDirectory string            `yaml:"working-directory"`
	Env              map[string]string `yaml:"env"`
}

// A step that runs inside a project directory and also hands that directory
// to the engine applies it twice: forge-ci build npm run, started in
// packages/frontend with WORKING_DIRECTORY=packages/frontend, looked for
// packages/frontend/packages/frontend/package.json, so every npm or Cargo
// project below the repository root failed on GitHub. The engine resolves the
// directory itself; the step runs from the workspace root. Workflows and
// composite actions of both forges are held to it, whatever expression
// spelling names the directory.
func TestNoStepAppliesItsWorkingDirectoryTwice(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	checked := 0

	for _, base := range []string{".github", ".forgejo"} {
		err := filepath.WalkDir(filepath.Join(root, base), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".yml" {
				return err
			}

			rel, _ := filepath.Rel(root, path)
			body, err := os.ReadFile(path)
			require.NoError(t, err)

			var file struct {
				Jobs map[string]struct {
					Defaults struct {
						Run struct {
							WorkingDirectory string `yaml:"working-directory"`
						} `yaml:"run"`
					} `yaml:"defaults"`
					Env   map[string]string `yaml:"env"`
					Steps []guardStep       `yaml:"steps"`
				} `yaml:"jobs"`
				Runs struct {
					Steps []guardStep `yaml:"steps"`
				} `yaml:"runs"`
			}
			require.NoError(t, yaml.Unmarshal(body, &file), rel)

			for jobName, job := range file.Jobs {
				for _, step := range job.Steps {
					checked++

					checkWorkingDirectoryOnce(t, rel+" job "+jobName, step, job.Defaults.Run.WorkingDirectory, job.Env)
				}
			}

			for _, step := range file.Runs.Steps {
				checked++

				checkWorkingDirectoryOnce(t, rel, step, "", nil)
			}

			return nil
		})
		require.NoError(t, err)
	}

	require.Positive(t, checked, "no step found; the guard checks nothing")
}

func checkWorkingDirectoryOnce(t *testing.T, where string, step guardStep, jobDefault string, jobEnv map[string]string) {
	t.Helper()

	dir := step.WorkingDirectory
	if dir == "" && step.Run != "" {
		dir = jobDefault
	}

	dir = canonicalExpression(dir)
	if dir == "" || dir == "." {
		return
	}

	env := map[string]string{}
	maps.Copy(env, jobEnv)

	maps.Copy(env, step.Env)

	for name, value := range env {
		if strings.HasSuffix(name, "WORKING_DIRECTORY") && canonicalExpression(value) == dir {
			t.Errorf("%s step %q runs in %s and passes it again as %s", where, step.Name, dir, name)
		}
	}
}

var (
	expressionIndex = regexp.MustCompile(`\b(inputs|env|vars|matrix)\[\s*['"]([^'"]+)['"]\s*\]`)
	expressionSpace = regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
)

// canonicalExpression spells one directory one way: inputs['x'],
// inputs["x"] and inputs.x are the same value, with or without spaces.
func canonicalExpression(value string) string {
	value = strings.TrimSpace(value)
	value = expressionIndex.ReplaceAllString(value, "$1.$2")

	return expressionSpace.ReplaceAllString(value, "$${{ $1 }}")
}

func TestCanonicalExpressionSpellsOneDirectoryOneWay(t *testing.T) {
	t.Parallel()

	for _, spelling := range []string{
		"${{ inputs.working-directory }}",
		"${{inputs.working-directory}}",
		"${{ inputs['working-directory'] }}",
		`${{ inputs["working-directory"] }}`,
		"  ${{   inputs[ 'working-directory' ] }} ",
	} {
		require.Equal(t, "${{ inputs.working-directory }}", canonicalExpression(spelling), spelling)
	}

	require.Equal(t, "packages/frontend", canonicalExpression(" packages/frontend "))
}
