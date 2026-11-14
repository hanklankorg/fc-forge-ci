// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/stretchr/testify/require"
)

func TestMavenSnapshotsAreOptInAndCoverBothGradleLibraryKinds(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	for _, kind := range []projecttype.Type{projecttype.Maven, projecttype.Gradle, projecttype.GradleAndroid} {
		cfg.Artifacts = append(cfg.Artifacts, config.Artifact{Name: string(kind), ProjectType: kind, BuildType: config.BuildTypeLibrary, PublishTo: []config.PublishTarget{config.PublishForgePackages, config.PublishMavenCentral}})
	}

	cfg.Artifacts = append(cfg.Artifacts, config.Artifact{Name: "npm", ProjectType: projecttype.NPM, PublishTo: []config.PublishTarget{config.PublishForgePackages}, NPM: &config.NPMConfig{Tag: "release-{version}"}})
	require.NoError(t, config.Derive(cfg))
	require.NoError(t, config.Validate(cfg))

	for _, enabled := range []bool{false, true} {
		plan, err := pipeline.NewSnapshotReleasePlan(pipeline.SnapshotReleasePlanInput{ConfigPlan: pipeline.NewConfigPlan(cfg), PublishMavenSnapshots: enabled})
		require.NoError(t, err)
		require.Equal(t, enabled, plan.Stages.Build.ExportPublications)
		require.Equal(t, enabled, plan.Stages.Publish.Targets.ForgePackages.Runs)
		require.Equal(t, enabled, plan.Stages.Publish.Targets.MavenCentral.Runs)
		require.Len(t, plan.Stages.Publish.Targets.ForgePackages.Items, 3)
		require.Len(t, plan.Stages.Publish.Targets.MavenCentral.Items, 3)
		require.False(t, plan.Stages.Publish.Targets.NPM.Runs)
	}
}
