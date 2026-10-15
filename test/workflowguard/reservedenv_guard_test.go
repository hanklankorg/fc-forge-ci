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

// notRunnerVariables are GITHUB_-prefixed names a workflow may set: a secret,
// and a name GitHub does not define.
var notRunnerVariables = map[string]bool{"GITHUB_TOKEN": true, "GITHUB_REPO": true} //nolint:gochecknoglobals // read-only table.

// Actions ignores a workflow's value for a GITHUB_* variable the runner sets.
// The release orchestrator passed the derived tag as GITHUB_REF_NAME; the
// planner read release-request/vX.Y.Z instead, and every GitHub release was a
// draft that was not marked latest. A value meant for the engine goes under a
// name the runner does not own (TAG_NAME, REF_NAME).
func TestWorkflowsSetNoRunnerOwnedVariable(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(reporoot.Path(t), ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // repository contract fixture.
		require.NoError(t, err)

		var workflow struct {
			Env  map[string]string `yaml:"env"`
			Jobs map[string]struct {
				Env   map[string]string `yaml:"env"`
				Steps []struct {
					Name string            `yaml:"name"`
					Env  map[string]string `yaml:"env"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(body, &workflow), entry.Name())

		check := func(where string, env map[string]string) {
			for name := range env {
				if strings.HasPrefix(name, "GITHUB_") && !notRunnerVariables[name] {
					t.Errorf("%s sets %s, which the runner owns and Actions does not let a workflow change", where, name)
				}
			}
		}

		check(entry.Name(), workflow.Env)

		for jobName, job := range workflow.Jobs {
			check(entry.Name()+" job "+jobName, job.Env)

			for _, step := range job.Steps {
				check(entry.Name()+" job "+jobName+" step "+step.Name, step.Env)
			}
		}
	}
}
