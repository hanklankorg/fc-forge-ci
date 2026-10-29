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
			Permissions map[string]string
			Steps       []struct {
				Env map[string]string
			}
		}
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
					require.Equal(t, "${{ github.event_name != 'pull_request' && format('ghcr.io/{0}-buildcache', github.repository) || '' }}", cache)
					require.Empty(t, step.Env["BUILD_CACHE_PUSH"])
				} else {
					require.Equal(t, "ghcr.io/${{ github.repository }}-buildcache", cache)
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
	require.Equal(t, "read", workflow.Jobs["verify"].Permissions["packages"])
	require.Equal(t, "write", workflow.Jobs["publish-arch"].Permissions["packages"])
}
