// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"encoding/json"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/stretchr/testify/require"
)

func TestSnapshotTransfersDeduplicateArchitecturesAndSupportMultipleDestinations(t *testing.T) {
	plan := fixture(t)
	cfg := &config.Config{Artifacts: []config.Artifact{{Name: "server", ProjectType: projecttype.Go, SBOMs: "none"}}}
	require.NoError(t, config.Validate(cfg))
	require.NoError(t, config.Derive(cfg))
	plan.Artifacts = pipeline.NewConfigPlan(cfg).Artifacts.All
	plan.SourceDateEpoch = "1"

	dep := snapshotimage.Dependency{Name: "server", ArtifactName: "server-go-build-artifacts", Destination: "dist", ProjectType: projecttype.Go}
	for i := range plan.Builds {
		plan.Builds[i].RequiredArtifacts = []snapshotimage.Dependency{dep}
	}

	transfers, err := plan.ArtifactTransfers()
	require.NoError(t, err)
	require.Len(t, transfers.Items, 1)

	image := plan.Images[0]
	image.Name = "other"
	image.ImageRepository += "/other"

	plan.Images = append(plan.Images, image)
	for _, build := range append([]snapshotimage.Build{}, plan.Builds...) {
		build.Name = image.Name
		build.ImageRepository = image.ImageRepository
		build.Context = "other"
		moved := dep
		moved.Destination = "other/dist"
		build.RequiredArtifacts = []snapshotimage.Dependency{moved}
		plan.Builds = append(plan.Builds, build)
	}

	transfers, err = plan.ArtifactTransfers()
	require.NoError(t, err)
	require.Len(t, transfers.Items, 1)
	require.Equal(t, "_snapshot-downloads/server", transfers.Items[0].Path)

	for _, item := range transfers.Items {
		require.Equal(t, pipeline.ArtifactTransferBuildArtifact, item.Kind)
		require.True(t, item.Required)
	}

	body, err := json.Marshal(transfers)
	require.NoError(t, err)
	_, err = pipeline.ParseArtifactTransferPlan(string(body))
	require.NoError(t, err)

	plan.Builds[1].RequiredArtifacts[0].ArtifactName = "different-upload"
	require.Error(t, plan.Validate())
	plan = fixture(t)
	dep.Destination = ".git/objects"
	plan.Builds[0].RequiredArtifacts = []snapshotimage.Dependency{dep}
	_, err = plan.ArtifactTransfers()
	require.ErrorContains(t, err, "reserved")
	plan = fixture(t)
	plan.Artifacts = pipeline.NewConfigPlan(cfg).Artifacts.All
	plan.SourceDateEpoch = "1"
	dep.Destination = "dist"
	plan.Builds[0].RequiredArtifacts = []snapshotimage.Dependency{dep}
	other := dep
	other.Destination = "dist/dist"
	plan.Builds[1].Context = "dist"
	plan.Builds[1].RequiredArtifacts = []snapshotimage.Dependency{other}
	_, err = plan.ArtifactTransfers()
	require.ErrorContains(t, err, "overlap")
}
