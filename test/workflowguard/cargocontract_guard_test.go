// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestSelectedCargoContractsReceiveOnlyTheVerifiedEnginePath(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				ID   string            `yaml:"id"`
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/build-cargo.yml"), &workflow))

	checkout, verified, build := -1, -1, -1

	for index, step := range workflow.Jobs["build-cargo"].Steps {
		switch {
		case step.With["path"] == ".forge-ci-contract-engine":
			checkout = index

			require.Equal(t, "${{ inputs['test-selection-json'] != '' }}", step.If)
			require.Equal(t, "actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10", step.Uses)
			require.Equal(t, "${{ needs.resolve-bootstrap.outputs.forge-ci-repository }}", step.With["repository"])
			require.Equal(t, "${{ needs.resolve-bootstrap.outputs.forge-ci-binary-ref }}", step.With["ref"])
			require.Equal(t, ".engine", step.With["sparse-checkout"])
			require.Equal(t, "false", step.With["persist-credentials"])
		case step.ID == "contract-engine":
			verified = index

			require.Equal(t, "${{ inputs['test-selection-json'] != '' }}", step.If)
			require.Equal(t, "/usr/local/bin/forge-ci", step.Env["FORGE_CI_BIN"])
			require.Equal(t, "1", step.Env["SIGNER_STRICT"])
			require.Equal(t, "set -euo pipefail\nsource .forge-ci-contract-engine/.engine/bin/lib-forge-ci.sh\nverify_forge_ci\nprintf 'binary-path=%s\\n' \"$HELPERS_ENGINE_VERIFIED\" >>\"$GITHUB_OUTPUT\"\n", step.Run)
		case step.Name == "Build Cargo project":
			build = index

			require.Equal(t, "${{ steps.contract-engine.outputs.binary-path }}", step.Env["FORGE_CI_CONTRACT_BIN"])
		}
	}

	require.GreaterOrEqual(t, checkout, 0)
	require.Greater(t, verified, checkout)
	require.Greater(t, build, verified)
}
