// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/stretchr/testify/require"
)

func TestAndroidLibraryTargetsAndDefaults(t *testing.T) {
	t.Parallel()

	artifact := config.Artifact{Name: "library", ProjectType: projecttype.GradleAndroid, BuildType: config.BuildTypeLibrary, PublishTo: []config.PublishTarget{config.PublishForgePackages, config.PublishMavenCentral}}
	cfg := &config.Config{Artifacts: []config.Artifact{artifact}}
	require.NoError(t, config.Derive(cfg))
	require.NoError(t, config.Validate(cfg))
	require.Equal(t, "build", cfg.Artifacts[0].SBOMs)
	require.False(t, artifact.IncludeAAB())
	require.Equal(t, "release", artifact.AndroidBuildTypes())
	require.False(t, config.SupportedPublishTarget(artifact, config.PublishGooglePlay))
}

func TestAndroidLibraryRejectsApplicationOptions(t *testing.T) {
	t.Parallel()

	enabled := true
	for _, options := range []config.GradleAndroidConfig{{IncludeAAB: &enabled}, {EnableAndroidSigning: &enabled}, {BuildTypes: "debug"}, {BuildTypes: "notrelease"}} {
		cfg := &config.Config{Artifacts: []config.Artifact{{Name: "library", ProjectType: projecttype.GradleAndroid, BuildType: config.BuildTypeLibrary, GradleAndroid: &options}}}
		require.Error(t, config.Validate(cfg))
	}
}

func TestAndroidLibraryRejectsUnsupportedSBOMBeforeBuilding(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Artifacts: []config.Artifact{{Name: "library", ProjectType: projecttype.GradleAndroid, BuildType: config.BuildTypeLibrary, SBOMs: "all"}}}
	require.ErrorContains(t, config.Validate(cfg), "analyzed-artifact")
}
