// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
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
	plan, err := snapshotimage.New(pipeline.ConfigPlan{Containers: pipeline.ContainerSets{All: []pipeline.PlannedContainer{{Name: "app", ContainerFile: "Containerfile", Context: ".", Platforms: "linux/amd64,linux/arm64", ImageName: "team/app", TagSuffix: "-slim"}}}}, snapshotimage.Input{SourceSHA: strings.Repeat("a", 40), SourceBranch: "manual-branch", Tag: tag, ImageRepository: "registry.example/team/base", Publish: true})
	require.NoError(t, err)

	return plan
}

func TestMatrixPolicyAndNameResolution(t *testing.T) {
	plan := fixture(t)
	require.Len(t, plan.Builds, 2)
	require.Equal(t, "registry.example/team/app", plan.Images[0].ImageRepository)
	require.Regexp(t, `^1\.2\.3-slim-manual-branch-[0-9a-f]{16}-aaaaaaa-snapshot$`, plan.Images[0].Tag)
	require.Equal(t, "NONE", plan.Images[0].Signing)
	require.True(t, plan.Images[0].Scan)

	for _, mutate := range []func(*snapshotimage.Plan){
		func(p *snapshotimage.Plan) { p.Builds = p.Builds[:1] },
		func(p *snapshotimage.Plan) { p.Builds[1].Arch = "amd64" },
		func(p *snapshotimage.Plan) { p.Images[0].Platforms[1] = "linux/arm/v7" },
		func(p *snapshotimage.Plan) { p.Builds[0].Context = "../outside" },
		func(p *snapshotimage.Plan) { p.Images[0].Provenance = true },
	} {
		plan = fixture(t)
		mutate(&plan)
		require.Error(t, plan.Validate())
	}
}

// A published snapshot image is always scanned, and a frozen plan without the
// scan is refused.
func TestEveryPlannedImageIsScanned(t *testing.T) {
	tag, err := version.ComposeSnapshotContainerTag("1.2.3", "manual-branch", "aaaaaaa")
	require.NoError(t, err)

	plan, err := snapshotimage.New(pipeline.ConfigPlan{Containers: pipeline.ContainerSets{All: []pipeline.PlannedContainer{{Name: "app", ContainerFile: "Containerfile", Context: ".", Platforms: "linux/amd64"}}}}, snapshotimage.Input{SourceSHA: strings.Repeat("a", 40), SourceBranch: "manual-branch", Tag: tag, ImageRepository: "registry.example/team/app", Publish: true})
	require.NoError(t, err)
	require.True(t, plan.Images[0].Scan)

	plan.Images[0].Scan = false
	require.ErrorContains(t, plan.Validate(), "without its scan")
}

// A declared binary extraction is a release's output: the snapshot builds the
// runtime image of the container and extracts nothing. Build secrets, which
// the build itself would need, stay refused.
func TestExtractionBuildsTheRuntimeImageAndSecretsAreRefused(t *testing.T) {
	tag, err := version.ComposeSnapshotContainerTag("1.2.3", "manual-branch", "aaaaaaa")
	require.NoError(t, err)

	plan := func(ctr pipeline.PlannedContainer) (snapshotimage.Plan, error) {
		ctr.Name, ctr.ContainerFile, ctr.Context, ctr.Platforms = "app", "Containerfile", ".", "linux/amd64"

		return snapshotimage.New(pipeline.ConfigPlan{Containers: pipeline.ContainerSets{All: []pipeline.PlannedContainer{ctr}}}, snapshotimage.Input{SourceSHA: strings.Repeat("a", 40), SourceBranch: "manual-branch", Tag: tag, ImageRepository: "registry.example/team/app", Publish: true})
	}

	for _, ctr := range []pipeline.PlannedContainer{
		{Target: "runtime", ExtractBinaryTarget: "export-binary", ExtractBinaryNames: []string{"app"}},
		{Target: "runtime", ExtractBinaryPaths: []string{"/usr/local/bin/app"}},
	} {
		p, err := plan(ctr)
		require.NoError(t, err)
		require.Len(t, p.Builds, 1)
		require.Equal(t, "runtime", p.Builds[0].Target)
	}

	_, err = plan(pipeline.PlannedContainer{BuildSecrets: []string{"TOKEN"}})
	require.ErrorContains(t, err, "declares build-secrets, and a snapshot builds branch code without secrets; set publish-containers to false")
}

// The snapshot plan reads scan-severity with the scan's own parser and records
// the normalised blocking threshold; a frozen plan without one is refused.
func TestScanSeverityIsTheNormalisedThreshold(t *testing.T) {
	tag, err := version.ComposeSnapshotContainerTag("1.2.3", "manual-branch", "aaaaaaa")
	require.NoError(t, err)

	plan := func(severity string) (snapshotimage.Plan, error) {
		return snapshotimage.New(pipeline.ConfigPlan{Containers: pipeline.ContainerSets{All: []pipeline.PlannedContainer{{Name: "app", ContainerFile: "Containerfile", Context: ".", Platforms: "linux/amd64", ScanSeverity: severity}}}}, snapshotimage.Input{SourceSHA: strings.Repeat("a", 40), SourceBranch: "manual-branch", Tag: tag, ImageRepository: "registry.example/team/app", Publish: true})
	}

	for severity, want := range map[string]string{"": "CRITICAL", "critical": "CRITICAL", "CRITICAL, high": "HIGH", "medium": "MEDIUM"} {
		p, err := plan(severity)
		require.NoError(t, err, severity)
		require.Equal(t, want, p.Images[0].Severity, severity)
	}

	_, err = plan("CRITICAL,SEVERE")
	require.ErrorContains(t, err, "unsupported snapshot scan severity")

	p, err := plan("")
	require.NoError(t, err)

	p.Images[0].Severity = ""
	require.Error(t, p.Validate())
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
	plan := fixture(t)
	receipt := snapshotimage.Receipt{Version: 1, SourceSHA: plan.SourceSHA, SourceBranch: plan.SourceBranch, Name: "app", ImageRepository: plan.Images[0].ImageRepository, Tag: plan.Images[0].Tag, Digest: "sha256:" + strings.Repeat("b", 64), Platforms: append([]string{}, plan.Images[0].Platforms...)}
	receipt.Ref = receipt.ImageRepository + "@" + receipt.Digest
	_, err := snapshotimage.Collect(plan, []snapshotimage.Receipt{receipt})
	require.NoError(t, err)
	_, err = snapshotimage.Collect(plan, nil)
	require.Error(t, err)
	_, err = snapshotimage.Collect(plan, []snapshotimage.Receipt{receipt, receipt})
	require.Error(t, err)

	for _, mutate := range []func(*snapshotimage.Receipt){
		func(r *snapshotimage.Receipt) { r.SourceSHA = strings.Repeat("c", 40) },
		func(r *snapshotimage.Receipt) { r.SourceBranch = "main" },
		func(r *snapshotimage.Receipt) { r.Ref = r.ImageRepository + ":latest" },
		func(r *snapshotimage.Receipt) { r.Digest = "sha256:bad" },
		func(r *snapshotimage.Receipt) { r.Platforms = []string{"linux/amd64"} },
		func(r *snapshotimage.Receipt) { r.Tag = "release" },
	} {
		bad := receipt
		mutate(&bad)
		_, err = snapshotimage.Collect(plan, []snapshotimage.Receipt{bad})
		require.Error(t, err)
	}

	other := plan.Images[0]
	other.Name = "other"
	other.ImageRepository += "/other"

	plan.Images = append(plan.Images, other)
	for _, build := range append([]snapshotimage.Build{}, plan.Builds...) {
		build.Name = other.Name
		build.ImageRepository = other.ImageRepository
		plan.Builds = append(plan.Builds, build)
	}

	_, err = snapshotimage.Collect(plan, []snapshotimage.Receipt{receipt, receipt})
	require.ErrorContains(t, err, "duplicate snapshot receipt")
}
