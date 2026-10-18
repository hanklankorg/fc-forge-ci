// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCentralDestinationRequiresAuthorization(t *testing.T) {
	t.Parallel()

	for _, target := range []config.PublishTarget{config.PublishForgePackages, config.PublishMavenCentral} {
		t.Run(string(target), func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{Artifacts: []config.Artifact{
				{Name: "first", ProjectType: projecttype.Go},
				{Name: "library", ProjectType: projecttype.Maven, BuildType: config.BuildTypeLibrary, PublishTo: []config.PublishTarget{target}},
			}}
			require.NoError(t, config.Derive(cfg))
			plan, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{ConfigPlan: pipeline.NewConfigPlan(cfg), ReleaseRequireAllowlistedSigner: false, ReleaseSBOMs: "all"})
			require.NoError(t, err)
			require.Equal(t, target == config.PublishMavenCentral, plan.Policy.RequireAllowlistedSigner)
		})
	}
}
