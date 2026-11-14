// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// An npm package holds the directories its package.json selects, dist/ in
// one project and .output/ in the next, so the image job checks that the
// package arrived and names no directory: it unpacks the package right after
// downloading it, under the same condition, and no step validates an npm
// artifact directory.
func TestImageJobUnpacksTheNPMPackageAndNamesNoDirectory(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				If   string `yaml:"if"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", "publish-container.yml"))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(body, &workflow))

	unpacks := 0

	for name, job := range workflow.Jobs {
		for index, step := range job.Steps {
			require.NotContains(t, step.Run, "--project-type npm", "%s step %q validates an npm artifact directory", name, step.Name)

			if !strings.Contains(step.Run, "container extract-npm-tarball") {
				continue
			}

			unpacks++

			require.Equal(t, "Unpack the npm package", step.Name, name)
			require.Equal(t, "forge-ci container extract-npm-tarball", strings.TrimSpace(step.Run), name)
			require.Positive(t, index, name)

			download := job.Steps[index-1]
			require.Equal(t, "Download NPM artifacts", download.Name, name)
			require.Contains(t, download.Run, "forge-ci artifact download", name)
			require.NotEmpty(t, step.If, name)
			require.Equal(t, download.If, step.If, "%s unpacks under another condition than it downloads", name)
		}
	}

	require.Equal(t, 1, unpacks, "publish-container.yml unpacks the npm package in one step")
}
