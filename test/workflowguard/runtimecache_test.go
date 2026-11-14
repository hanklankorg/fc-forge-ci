// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// A scratch engine must not export layers into a package owned by a different
// repository's token. Import and export must agree, and verification stays
// read-only with no registry cache on pull requests.
func TestSelfRuntimeCacheIsRepositoryScoped(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/self-runtime-container.yml"), &workflow))

	for _, id := range []string{"verify", "publish-arch"} {
		job, ok := workflow.Jobs[id]
		require.True(t, ok)

		caches, logins := 0, 0

		for _, step := range job.Steps {
			if cache, ok := step.Env["BUILD_CACHE_REPO"]; ok {
				caches++

				if id == "verify" {
					require.Equal(t, "${{ github.event_name != 'pull_request' && needs.image-coordinates.outputs.build-cache-repo || '' }}", cache)
					require.Empty(t, step.Env["BUILD_CACHE_PUSH"])
				} else {
					require.Equal(t, "${{ needs.image-coordinates.outputs.build-cache-repo }}", cache)
					require.Equal(t, "true", step.Env["BUILD_CACHE_PUSH"])
				}
			}

			if password, ok := step.Env["REGISTRY_PASSWORD"]; ok {
				logins++

				require.Equal(t, "${{ secrets.GITHUB_TOKEN }}", password)
			}
		}

		require.Equal(t, 1, caches, id)
		require.Equal(t, 1, logins, id)
	}

	// The repository-scoped cache, lowercased once for every job.
	require.Contains(t, string(reporoot.ReadFile(t, ".github/workflows/self-runtime-container.yml")), `printf 'build-cache-repo=ghcr.io/%s-buildcache\n' "${GITHUB_REPOSITORY,,}"`)
	require.Equal(t, "read", workflow.Jobs["verify"].Permissions["packages"])
	require.Equal(t, "write", workflow.Jobs["publish-arch"].Permissions["packages"])
}
