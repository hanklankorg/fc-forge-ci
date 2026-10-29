// SPDX-FileCopyrightText: The Forge CI Authors
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
	p := fixture(t)
	cfg := &config.Config{Artifacts: []config.Artifact{{Name: "server", ProjectType: projecttype.Go, SBOMs: "none"}}}
	require.NoError(t, config.Validate(cfg))
	require.NoError(t, config.Derive(cfg))
	p.Artifacts = pipeline.NewConfigPlan(cfg).Artifacts.All
	p.SourceDateEpoch = "1"
	dep := snapshotimage.Dependency{Name: "server", ArtifactName: "server-go-build-artifacts", Destination: "dist", ProjectType: projecttype.Go}
	for i := range p.Builds {
		p.Builds[i].RequiredArtifacts = []snapshotimage.Dependency{dep}
	}
	transfers, err := p.ArtifactTransfers()
	require.NoError(t, err)
	require.Len(t, transfers.Items, 1)
	image := p.Images[0]
	image.Name = "other"
	image.ImageRepository += "/other"
	p.Images = append(p.Images, image)
	for _, build := range append([]snapshotimage.Build{}, p.Builds...) {
		build.Name = image.Name
		build.ImageRepository = image.ImageRepository
		build.Context = "other"
		copy := dep
		copy.Destination = "other/dist"
		build.RequiredArtifacts = []snapshotimage.Dependency{copy}
		p.Builds = append(p.Builds, build)
	}
	transfers, err = p.ArtifactTransfers()
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
	p.Builds[1].RequiredArtifacts[0].ArtifactName = "different-upload"
	require.Error(t, p.Validate())
	p = fixture(t)
	dep.Destination = ".git/objects"
	p.Builds[0].RequiredArtifacts = []snapshotimage.Dependency{dep}
	_, err = p.ArtifactTransfers()
	require.ErrorContains(t, err, "reserved")
	p = fixture(t)
	p.Artifacts = pipeline.NewConfigPlan(cfg).Artifacts.All
	p.SourceDateEpoch = "1"
	dep.Destination = "dist"
	p.Builds[0].RequiredArtifacts = []snapshotimage.Dependency{dep}
	other := dep
	other.Destination = "dist/dist"
	p.Builds[1].Context = "dist"
	p.Builds[1].RequiredArtifacts = []snapshotimage.Dependency{other}
	_, err = p.ArtifactTransfers()
	require.ErrorContains(t, err, "overlap")
}
