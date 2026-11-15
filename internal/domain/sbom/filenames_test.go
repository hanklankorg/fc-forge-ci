// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/sbom"
)

func TestSBOMFilename_FromNameVersionLayer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format sbom.Format
		want   string
	}{
		{"spdx_format", sbom.FormatSPDX, "demo-1.2.3-build-sbom.spdx.json"},
		{"cyclonedx_format", sbom.FormatCycloneDX, "demo-1.2.3-build-sbom.cyclonedx.json"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := sbom.Filename("", "demo", "1.2.3", "build", testCase.format)
			require.Equal(t, testCase.want, got)
		})
	}
}

func TestSBOMFilename_FromCustomBasename(t *testing.T) {
	t.Parallel()

	got := sbom.Filename(
		"demo-1.2.3-abc1234-analyzed-jar",
		"ignored", "ignored", "analyzed-jar",
		sbom.FormatCycloneDX,
	)
	require.Equal(t, "demo-1.2.3-abc1234-analyzed-jar-sbom.cyclonedx.json", got)
}

func TestAnalyzedBasename_WithAndWithoutSHA(t *testing.T) {
	t.Parallel()
	require.Equal(t,
		"demo-1.2.3-abc1234-analyzed-jar",
		sbom.AnalyzedBasename("demo-1.2.3", "analyzed-jar", "abc1234"),
	)
	require.Equal(t,
		"demo-1.2.3-analyzed-jar",
		sbom.AnalyzedBasename("demo-1.2.3", "analyzed-jar", ""),
	)
}

func TestBuildLayerFilename_WithAndWithoutSHA(t *testing.T) {
	t.Parallel()
	require.Equal(t,
		"demo-1.2.3-abc1234-build-sbom.cyclonedx.json",
		sbom.BuildLayerFilename("demo", "1.2.3", "abc1234"),
	)
	require.Equal(t,
		"demo-1.2.3-build-sbom.cyclonedx.json",
		sbom.BuildLayerFilename("demo", "1.2.3", ""),
	)
}

func TestImageSBOMFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sha, slot string
		format    sbom.Format
		want      string
	}{
		{"abc1234", "linux/arm64", sbom.FormatCycloneDX, "app-1.2.3-abc1234-analyzed-container-linux-arm64-sbom.cyclonedx.json"},
		{"abc1234", "linux/amd64", sbom.FormatSPDX, "app-1.2.3-abc1234-analyzed-container-linux-amd64-sbom.spdx.json"},
		{"abc1234", sbom.ImageSBOMIndexSlot, sbom.FormatCycloneDX, "app-1.2.3-abc1234-analyzed-container-index-sbom.cyclonedx.json"},
		{"", "linux/arm/v7", sbom.FormatCycloneDX, "app-1.2.3-analyzed-container-linux-arm-v7-sbom.cyclonedx.json"},
	}
	for _, testCase := range tests {
		t.Run(testCase.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, testCase.want, sbom.ImageSBOMFilename("app", "1.2.3", testCase.sha, testCase.slot, testCase.format))
		})
	}
}
