// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

func TestSnapshotNativeArtifactsForwardFrozenCommitToConsumedEnvironment(t *testing.T) {
	t.Parallel()
	stage, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/release-snapshot-build-stage.yml"))
	require.NoError(t, err)
	for _, kind := range []string{"go", "cargo"} {
		t.Run(kind, func(t *testing.T) {
			job := mappingChild(mappingChild(stage, "jobs"), kind)
			require.Equal(t, "./.github/workflows/build-"+kind+".yml", scalarChild(job, "uses"))
			with := mappingChild(job, "with")
			frozenSHA := "${{ fromJson(inputs['snapshot-release-plan-json']).context.release_sha }}"
			require.Equal(t, frozenSHA, scalarChild(with, "branch"))
			require.Equal(t, frozenSHA, scalarChild(with, "commit"))
			leaf, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/build-"+kind+".yml"))
			require.NoError(t, err)
			steps := mappingChild(mappingChild(mappingChild(leaf, "jobs"), "build-"+kind), "steps")
			found := false
			for _, step := range steps.Content {
				if scalarChild(step, "run") != "forge-ci build "+kind+" run" {
					continue
				}
				found = true
				env := mappingChild(step, "env")
				require.Equal(t, "${{ inputs.commit }}", scalarChild(env, "CI_COMMIT"))
				require.Empty(t, scalarChild(env, "COMMIT"), "COMMIT is not a build CLI flag source")
			}
			require.True(t, found, "inspect the actual ecosystem build step")
		})
	}
}
