// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"regexp"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestOwnerDerivedImagePathsAreLowercased refuses an image path built from
// the owner or repository as GitHub spells it: an OCI repository path cannot
// hold capitals, and an expression cannot lowercase, so these are derived in a
// shell step and passed on.
func TestOwnerDerivedImagePathsAreLowercased(t *testing.T) {
	t.Parallel()

	unlowered := regexp.MustCompile(`format\('[^']*ghcr\.io[^']*', github\.repository(_owner)?\)|ghcr\.io/\$\{\{ github\.repository(_owner)? \}\}`)

	for _, path := range yamlFiles(t, ".github/workflows") {
		if match := unlowered.Find(reporoot.ReadFile(t, path)); match != nil {
			t.Errorf("%s builds an image path from the owner or repository unlowercased: %s", path, match)
		}
	}
}

func TestRuntimePublicationNamespaceCoherence(t *testing.T) {
	t.Parallel()

	const namespace = "${{ needs.image-coordinates.outputs.image-namespace }}"

	text := string(reporoot.ReadFile(t, ".github/workflows/self-runtime-container.yml"))

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
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

	require.Equal(t, 7, count, "build-and-scan, readback, SBOM, attestation, metadata, merge and retention")
	require.Contains(t, text, "org.opencontainers.image.source=https://github.com/${{ github.repository }}")
	require.Contains(t, text, "BUILD_CACHE_REPO: ${{ needs.image-coordinates.outputs.build-cache-repo }}")
	require.Contains(t, text, `"${IMAGE_NAMESPACE:-ghcr.io/${GITHUB_REPOSITORY_OWNER,,}}"`)
	require.Contains(t, text, `"${GITHUB_REPOSITORY,,}"`)
	require.Contains(t, text, "forge-ci container validate namespace")
}
