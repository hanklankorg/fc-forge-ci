// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The plan leaves an unset option out of its JSON, so a bare read of one is
// null. A build stage that forwards it to a boolean input of the workflow it
// calls must settle it first, as `|| false` or a comparison does.
func TestBuildStagesSettleOptionalPlanBooleans(t *testing.T) {
	t.Parallel()

	bare := regexp.MustCompile(`^\$\{\{\s*matrix\.artifact\.[a-z_]+\.[a-z_]+\s*\}\}$`)
	checked := 0

	for _, stage := range []string{"release-build-stage.yml", "release-snapshot-build-stage.yml"} {
		var caller struct {
			Jobs map[string]struct {
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/"+stage), &caller))

		for name, job := range caller.Jobs {
			if !strings.HasPrefix(job.Uses, "./.github/workflows/") {
				continue
			}

			var callee struct {
				On struct {
					WorkflowCall struct {
						Inputs map[string]struct {
							Type string `yaml:"type"`
						} `yaml:"inputs"`
					} `yaml:"workflow_call"`
				} `yaml:"on"`
			}
			require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, strings.TrimPrefix(job.Uses, "./")), &callee))

			for input, value := range job.With {
				if callee.On.WorkflowCall.Inputs[input].Type != "boolean" {
					continue
				}

				checked++

				require.NotRegexp(t, bare, value, "%s job %s passes %s a plan option that may be absent", stage, name, input)
			}
		}
	}

	require.Positive(t, checked)
}
