// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestContainerPublicationWaitsForCompleteBuildMatrix(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]struct {
			Needs       any               `yaml:"needs"`
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/publish-container.yml"), &workflow))
	build := workflow.Jobs["build-arch"]
	require.Equal(t, "read", build.Permissions["packages"])

	foundStage, foundRecord, foundUpload := false, false, false

	for _, step := range build.Steps {
		if strings.Contains(step.Run, "plan write") {
			require.Equal(t, "${{ inputs['push-image'] && 'oci' || 'load' }}", step.Env["BUILD_MODE"])
			require.Equal(t, "false", step.Env["BUILD_CACHE_PUSH"])
			require.Contains(t, step.Run, `--set "output-dir=${RUNNER_TEMP}/image-layout"`)

			foundStage = true
		}

		if strings.Contains(step.Run, "container layout record") {
			require.Equal(t, "${{ needs.prep.outputs.source-sha }}", step.Env["SOURCE_SHA"])

			foundRecord = true
		}

		if step.With["path"] == "${{ runner.temp }}/image-layout/" {
			require.Equal(t, "error", step.With["if-no-files-found"])

			foundUpload = true
		}

		require.NotContains(t, step.Run, "container attest")
		require.NotContains(t, step.Run, "layout publish")
	}

	require.True(t, foundStage && foundRecord && foundUpload)

	publish := workflow.Jobs["publish-images"]
	require.Equal(t, []any{"resolve-bootstrap", "prep", "build-arch"}, publish.Needs)
	require.Equal(t, "${{ inputs['push-image'] }}", publish.If, "no always() bypass of a failed matrix")

	foundPublish := false

	for _, step := range publish.Steps {
		if strings.Contains(step.Run, "container layout publish") {
			require.Equal(t, "${{ join(fromJson(needs.prep.outputs.platforms-json), ',') }}", step.Env["PLATFORMS"])
			require.Equal(t, "${{ needs.prep.outputs.source-sha }}", step.Env["SOURCE_SHA"])

			foundPublish = true
		}
	}

	require.True(t, foundPublish)
	require.Equal(t, []any{"resolve-bootstrap", "prep", "publish-images"}, workflow.Jobs["image-evidence"].Needs)
	require.Equal(t, []any{"resolve-bootstrap", "prep", "image-evidence"}, workflow.Jobs["merge"].Needs)
}
