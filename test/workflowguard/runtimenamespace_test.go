// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRuntimePublicationNamespaceCoherence(t *testing.T) {
	t.Parallel()
	const namespace = "${{ vars.FORGE_CI_IMAGE_NAMESPACE || format('ghcr.io/{0}', github.repository_owner) }}"
	text := string(reporoot.ReadFile(t, ".github/workflows/self-runtime-container.yml"))
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string
				Env  map[string]string
				With map[string]string
			}
		}
	}
	require.NoError(t, yaml.Unmarshal([]byte(text), &workflow))
	count := 0
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			for _, values := range []map[string]string{step.Env, step.With} {
				for _, key := range []string{"IMAGE_REF", "IMAGE_NAME", "RUNTIME_IMAGE", "image-ref"} {
					value := values[key]
					if !strings.Contains(value, "matrix.runtime.name") {
						continue
					}
					count++
					require.True(t, strings.HasPrefix(value, namespace+"/${{ matrix.runtime.name }}"), step.Name)
				}
			}
		}
	}
	require.Equal(t, 8, count, "build, readback, SBOM, attestation, scan, metadata, merge and retention")
	require.Contains(t, text, "org.opencontainers.image.source=https://github.com/${{ github.repository }}")
	require.Contains(t, text, "BUILD_CACHE_REPO: ghcr.io/${{ github.repository }}-buildcache")
	require.NotContains(t, text, "ghcr.io/${{ github.repository_owner }}/")
	require.Contains(t, text, "forge-ci container validate namespace")
}
