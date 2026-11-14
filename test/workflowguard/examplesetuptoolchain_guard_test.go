// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// setup-toolchain installs a consumer's mise tools only with a GitHub token
// or from a committed mise.lock (mise-locked); with neither it refuses a
// repository that has a mise config. The maven-app, cargo-app and go-cli
// Forgejo examples passed neither, so a release copied from them either
// refused or ran without the JDK or toolchain its build needs. The examples
// pass a read-only token: a locked install needs a lock URL for every tool.
// dependencyGitHubToken is the documented read-only token of a consumer's
// own tool installs (docs/forgejo.md, "The dependency token of your own jobs").
const dependencyGitHubToken = "${{ secrets.DEPENDENCY_GITHUB_TOKEN }}"

func TestForgejoExampleToolchainsInstallFromALockOrAToken(t *testing.T) {
	t.Parallel()

	// Every Forgejo workflow under examples/, at any depth (signing/*/forgejo,
	// examples/forgejo), so a new example is held as soon as it lands.
	var files []string

	err := filepath.WalkDir(filepath.Join(reporoot.Path(t), "examples"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr == nil && !d.IsDir() && filepath.Ext(path) == ".yml" &&
			strings.Contains(path, string(filepath.Separator)+"forgejo"+string(filepath.Separator)) {
			files = append(files, path)
		}

		return walkErr
	})
	require.NoError(t, err)

	found := 0

	for _, path := range files {
		body, err := os.ReadFile(path)
		require.NoError(t, err)

		var workflow struct {
			Jobs map[string]struct {
				Steps []struct {
					Name string            `yaml:"name"`
					Uses string            `yaml:"uses"`
					With map[string]string `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(body, &workflow), path)

		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if !strings.Contains(step.Uses, "/setup-toolchain@") {
					continue
				}

				found++

				token := strings.TrimSpace(step.With["mise-github-token"])
				if step.With["mise-locked"] != "true" && token == "" {
					t.Errorf("%s job %s step %q installs neither from mise.lock (mise-locked: 'true') nor with mise-github-token", path, jobName, step.Name)
				}

				// One name everywhere, so one organization secret serves
				// every repository copied from an example.
				if token != "" && token != dependencyGitHubToken {
					t.Errorf("%s job %s step %q passes mise-github-token %q, want %s", path, jobName, step.Name, token, dependencyGitHubToken)
				}
			}
		}
	}

	require.Positive(t, found, "no Forgejo example calls setup-toolchain; the guard checks nothing")
}
