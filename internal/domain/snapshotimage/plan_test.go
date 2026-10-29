// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T) snapshotimage.Plan {
	t.Helper()
	tag, err := version.ComposeSnapshotContainerTag("1.2.3", "manual-branch", "aaaaaaa")
	require.NoError(t, err)
	p, err := snapshotimage.New(pipeline.ConfigPlan{Containers: pipeline.ContainerSets{All: []pipeline.PlannedContainer{{Name: "app", ContainerFile: "Containerfile", Context: ".", Platforms: "linux/amd64,linux/arm64", EnableScan: true, ImageName: "team/app", TagSuffix: "-slim"}}}}, snapshotimage.Input{SourceSHA: strings.Repeat("a", 40), SourceBranch: "manual-branch", Tag: tag, ImageRepository: "registry.example/team/base", Publish: true})
	require.NoError(t, err)
	return p
}

func TestMatrixPolicyAndNameResolution(t *testing.T) {
	p := fixture(t)
	require.Len(t, p.Builds, 2)
	require.Equal(t, "registry.example/team/app", p.Images[0].ImageRepository)
	require.Regexp(t, `^1\.2\.3-slim-manual-branch-[0-9a-f]{16}-aaaaaaa-snapshot$`, p.Images[0].Tag)
	require.Equal(t, "NONE", p.Images[0].Signing)
	require.True(t, p.Images[0].Scan)
	for _, mutate := range []func(*snapshotimage.Plan){
		func(p *snapshotimage.Plan) { p.Builds = p.Builds[:1] },
		func(p *snapshotimage.Plan) { p.Builds[1].Arch = "amd64" },
		func(p *snapshotimage.Plan) { p.Images[0].Platforms[1] = "linux/arm/v7" },
		func(p *snapshotimage.Plan) { p.Builds[0].Context = "../outside" },
		func(p *snapshotimage.Plan) { p.Images[0].Provenance = true },
	} {
		p = fixture(t)
		mutate(&p)
		require.Error(t, p.Validate())
	}
}

func TestFrozenImageTagsRejectCoordinatedBuildAndImageMutation(t *testing.T) {
	for _, branch := range []string{"manual-branch", strings.Repeat("long-branch-", 40)} {
		for _, suffix := range []string{"", "-slim"} {
			tag, err := version.ComposeSnapshotContainerTag("1.2.3", branch, "aaaaaaa")
			require.NoError(t, err)
			p, err := snapshotimage.New(pipeline.ConfigPlan{Containers: pipeline.ContainerSets{All: []pipeline.PlannedContainer{{Name: "app", ContainerFile: "Containerfile", Context: ".", Platforms: "linux/amd64", TagSuffix: suffix}}}}, snapshotimage.Input{SourceSHA: strings.Repeat("a", 40), SourceBranch: branch, Tag: tag, ImageRepository: "registry.example/team/app", Publish: true})
			require.NoError(t, err)
			require.NoError(t, p.Validate())
			require.Equal(t, "1.2.3", p.BaseVersion)
			require.Equal(t, suffix, p.Images[0].TagSuffix)
			wrong, err := version.ComposeSnapshotContainerTag("1.2.3"+suffix, "another-branch", "aaaaaaa")
			require.NoError(t, err)
			p.Images[0].Tag = wrong
			p.Builds[0].Tag = wrong
			require.ErrorContains(t, p.Validate(), "frozen base, suffix or branch")
		}
	}
	p := fixture(t)
	p.BaseVersion = "9.9.9"
	require.Error(t, p.Validate())
}

func TestSnapshotReceiptsAreAnExactIdentityBarrier(t *testing.T) {
	p := fixture(t)
	r := snapshotimage.Receipt{Version: 1, SourceSHA: p.SourceSHA, SourceBranch: p.SourceBranch, Name: "app", ImageRepository: p.Images[0].ImageRepository, Tag: p.Images[0].Tag, Digest: "sha256:" + strings.Repeat("b", 64), Platforms: append([]string{}, p.Images[0].Platforms...)}
	r.Ref = r.ImageRepository + "@" + r.Digest
	_, err := snapshotimage.Collect(p, []snapshotimage.Receipt{r})
	require.NoError(t, err)
	_, err = snapshotimage.Collect(p, nil)
	require.Error(t, err)
	_, err = snapshotimage.Collect(p, []snapshotimage.Receipt{r, r})
	require.Error(t, err)
	for _, mutate := range []func(*snapshotimage.Receipt){
		func(r *snapshotimage.Receipt) { r.SourceSHA = strings.Repeat("c", 40) },
		func(r *snapshotimage.Receipt) { r.SourceBranch = "main" },
		func(r *snapshotimage.Receipt) { r.Ref = r.ImageRepository + ":latest" },
		func(r *snapshotimage.Receipt) { r.Digest = "sha256:bad" },
		func(r *snapshotimage.Receipt) { r.Platforms = []string{"linux/amd64"} },
		func(r *snapshotimage.Receipt) { r.Tag = "release" },
	} {
		bad := r
		mutate(&bad)
		_, err = snapshotimage.Collect(p, []snapshotimage.Receipt{bad})
		require.Error(t, err)
	}
	other := p.Images[0]
	other.Name = "other"
	other.ImageRepository += "/other"
	p.Images = append(p.Images, other)
	for _, build := range append([]snapshotimage.Build{}, p.Builds...) {
		build.Name = other.Name
		build.ImageRepository = other.ImageRepository
		p.Builds = append(p.Builds, build)
	}
	_, err = snapshotimage.Collect(p, []snapshotimage.Receipt{r, r})
	require.ErrorContains(t, err, "duplicate snapshot receipt")
}
