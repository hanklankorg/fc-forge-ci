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

// `release validate-tag` reads the remote tag with the forge's ambient token,
// GITHUB_TOKEN on GitHub. The orchestrator once gave it RELEASE_TOKEN, which
// the command never reads: a public repository passed anonymously, and a
// private one failed with "could not read Username". Every GitHub step that
// runs it passes the job's token, and no release write token it would ignore.
func TestValidateTagStepsPassTheJobToken(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(reporoot.Path(t), ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	checked := 0

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // repository contract fixture.
		require.NoError(t, err)

		var workflow struct {
			Jobs map[string]struct {
				Steps []struct {
					Name string            `yaml:"name"`
					Run  string            `yaml:"run"`
					Env  map[string]string `yaml:"env"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(body, &workflow), entry.Name())

		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if !strings.Contains(step.Run, "forge-ci release validate-tag") {
					continue
				}

				checked++

				if step.Env["GITHUB_TOKEN"] != "${{ github.token }}" {
					t.Errorf("%s job %q step %q runs validate-tag without the job's GITHUB_TOKEN", entry.Name(), jobName, step.Name)
				}

				if _, set := step.Env["RELEASE_TOKEN"]; set {
					t.Errorf("%s job %q step %q hands validate-tag a RELEASE_TOKEN it does not read", entry.Name(), jobName, step.Name)
				}
			}
		}
	}

	require.Positive(t, checked, "no GitHub step runs validate-tag; the guard has drifted")
}
