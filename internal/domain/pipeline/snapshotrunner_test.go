// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
)

func TestSnapshotRunnerAdmissionUsesDeclaredPlatforms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		cfg   config.Config
		input string
	}{
		{"image", config.Config{
			Artifacts:  []config.Artifact{{Name: "service", ProjectType: projecttype.Go}},
			Containers: []config.Container{{Name: "image", Platforms: "linux/arm64"}},
		}, "build-runs-on-linux-arm64"},
		{"xcode", config.Config{
			Artifacts: []config.Artifact{{Name: "ios", ProjectType: projecttype.XcodeIOS, XcodeIOS: &config.XcodeIOSConfig{EnableCodeSigning: new(false)}}},
		}, "runs-on-darwin-arm64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, config.Derive(&tc.cfg))
			in := pipeline.SnapshotReleasePlanInput{ConfigPlan: pipeline.NewConfigPlan(&tc.cfg), Branch: "main", SBOMs: "none"}
			legacy, err := pipeline.NewSnapshotReleasePlan(in)
			require.NoError(t, err, "omission stays compatible until every layer migrates")

			for _, missing := range []string{"", "[]"} {
				in.Runners, err = runners.ParseInputs(provider.ForgeGitHub, []string{`runs-on=["ordinary"]`, tc.input + "=" + missing})
				require.NoError(t, err)
				refused, err := pipeline.NewSnapshotReleasePlan(in)
				require.ErrorIs(t, err, errs.ErrUsage)
				require.ErrorContains(t, err, tc.input)
				require.Equal(t, pipeline.SnapshotReleasePlan{}, refused)
			}

			in.Runners, err = runners.ParseInputs(provider.ForgeGitHub, []string{tc.input + `=["isolated", "native"]`})
			require.NoError(t, err)
			admitted, err := pipeline.NewSnapshotReleasePlan(in)
			require.NoError(t, err)
			require.Equal(t, legacy, admitted, "runner admission must not rewrite the declaration or serialized plan")
		})
	}
}
