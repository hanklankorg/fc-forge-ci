// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom

import (
	"fmt"
	"strings"
)

// Format is one of the two SBOM document formats syft emits.
type Format string

// Recognised Format values.
const (
	FormatSPDX      Format = "spdx-json"
	FormatCycloneDX Format = "cyclonedx-json"
)

// ImageSBOMIndexSlot is the slot ImageSBOMFilename gives an index SBOM, the
// SBOM of a multi-platform image index.
const ImageSBOMIndexSlot = "index"

// formatSuffix is the file name suffix of a format.
func formatSuffix(format Format) string {
	if format == FormatCycloneDX {
		return "cyclonedx.json"
	}

	return "spdx.json"
}

// Filename returns the canonical SBOM filename for a layer.
// When customBasename is non-empty, the file is named
// "<customBasename>-sbom.<format-suffix>"; otherwise
// "<name>-<version>-<layer>-sbom.<format-suffix>" is used.
func Filename(customBasename, name, version, layer string, format Format) string {
	suffix := formatSuffix(format)

	if customBasename != "" {
		return fmt.Sprintf("%s-sbom.%s", customBasename, suffix)
	}

	return fmt.Sprintf("%s-%s-%s-sbom.%s", name, version, layer, suffix)
}

// AnalyzedBasename builds the basename for analyzed-* SBOM filenames.
// When sha is non-empty (i.e. inside a git repo), the short SHA is
// injected for traceability: "<file>-<sha>-<layer>". Otherwise:
// "<file>-<layer>" (SHA-less form for test fixtures without git).
func AnalyzedBasename(fileBasename, layer, sha string) string {
	if sha != "" {
		return fmt.Sprintf("%s-%s-%s", fileBasename, sha, layer)
	}

	return fmt.Sprintf("%s-%s", fileBasename, layer)
}

// BuildLayerFilename returns the Build-layer SBOM filename. When sha
// is non-empty the short SHA is injected; otherwise the SHA-less
// fallback is used.
func BuildLayerFilename(name, version, sha string) string {
	if sha != "" {
		return fmt.Sprintf("%s-%s-%s-build-sbom.cyclonedx.json", name, version, sha)
	}

	return fmt.Sprintf("%s-%s-build-sbom.cyclonedx.json", name, version)
}

// ImageSBOMFilename names an image SBOM in the layer naming of every other
// SBOM in a release's SBOM bundle, with the slot the SBOM describes after the
// layer: "<name>-<version>-<sha>-analyzed-container-<slot>-sbom.<format-suffix>".
// The slot is a platform such as linux/arm64, written "linux-arm64", or
// ImageSBOMIndexSlot for an index SBOM. The name ends in -sbom.<format-suffix>
// as every SBOM's does. An empty sha is left out, as AnalyzedBasename leaves
// it out.
func ImageSBOMFilename(name, version, sha, slot string, format Format) string {
	layer := string(LayerAnalyzedContainer) + "-" + strings.ReplaceAll(slot, "/", "-")

	return Filename(AnalyzedBasename(name+"-"+version, layer, sha), "", "", "", format)
}
