// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	appplan "github.com/diggsweden/forge-ci/internal/app/plan"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestReleaseExportsUseStrictSharedPlatformSelection(t *testing.T) {
	t.Parallel()

	for _, selection := range []string{"linux/amd64,linux/amd64", "linux/arm64, linux/amd64", "darwin/arm64", "linux/386"} {
		_, err := appplan.ReleaseExportPlatforms(selection)
		require.Error(t, err)
	}

	platforms, err := appplan.ReleaseExportPlatforms("")
	require.NoError(t, err)
	require.Equal(t, []string{"linux/amd64"}, platforms)

	for _, scenario := range []string{"valid", "spaced-arm-plan", "malformed-plan", "malformed-tags", "commands-not-data"} {
		in := appplan.ReleaseExportsInput{Enabled: "true", JobImage: "registry.example/engine@sha256:" + strings.Repeat("a", 64), ARM64Tags: "[]", Commands: `["do not execute"]`}

		switch scenario {
		case "spaced-arm-plan":
			in.ImagePlan = []byte("{\"builds\":[{\"arch\" : \"arm64\"}]}")
		case "malformed-plan":
			in.ImagePlan = []byte("bad")
		case "malformed-tags":
			in.ARM64Tags = "[not json]"
		case "commands-not-data":
			in.Commands = "touch should-not-execute"
		}

		err := appplan.CheckReleaseExports(in)
		if scenario == "valid" {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}
