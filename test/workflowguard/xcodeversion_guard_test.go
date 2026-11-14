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

// A caller that keeps its Xcode version outside the repository passes it as
// xcode-version, and it wins over the declaration's: each orchestrator hands
// its input to its build stage, and each build stage calls the Xcode build
// with the input first and the declared version as the fallback.
func TestCallerXcodeVersionReachesTheXcodeBuild(t *testing.T) {
	t.Parallel()

	type calls struct {
		Jobs map[string]struct {
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"jobs"`
	}

	const (
		input    = "${{ inputs['xcode-version'] }}"
		effected = "${{ inputs['xcode-version'] || matrix.artifact.xcode_ios.xcode_version }}"
	)

	for orchestrator, stage := range map[string]string{
		"release-orchestrator.yml":          "release-build-stage.yml",
		"release-snapshot-orchestrator.yml": "release-snapshot-build-stage.yml",
	} {
		for file, want := range map[string]struct{ callee, value string }{
			orchestrator: {"/" + stage, input},
			stage:        {"/build-xcode-ios.yml", effected},
		} {
			var workflow calls

			body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", file))
			require.NoError(t, err)
			require.NoError(t, yaml.Unmarshal(body, &workflow))

			found := 0

			for name, job := range workflow.Jobs {
				if strings.HasSuffix(job.Uses, want.callee) {
					found++

					require.Equal(t, want.value, job.With["xcode-version"], "%s job %s", file, name)
				}
			}

			require.Positive(t, found, "%s calls %s", file, want.callee)
		}
	}
}
