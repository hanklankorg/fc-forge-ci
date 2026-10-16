// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// NanolinterImageRepository is the registry repository of the nanolinter
// flavour images. Each flavour tag bakes the check toolchain of one ecosystem.
const NanolinterImageRepository = "ghcr.io/itiquette/nanolinter"

// NanolinterCoreFlavour is the ecosystem-neutral flavour: the checks every
// repository gets (secrets, SAST, dependency scans, text and CI files). It is
// the fallback for project types without an ecosystem flavour.
const NanolinterCoreFlavour = "core"

// NanolinterFlavour returns the nanolinter image flavour that carries the
// toolchain of the given project type. Types without a dedicated flavour
// (python, meta, unknown) get the core flavour.
func NanolinterFlavour(pt projecttype.Type) string {
	switch pt {
	case projecttype.Go:
		return "go"
	case projecttype.Maven:
		return "java"
	case projecttype.Gradle, projecttype.GradleAndroid:
		return "kotlin"
	case projecttype.NPM:
		return "javascript-typescript"
	case projecttype.Cargo:
		return "rust"
	case projecttype.XcodeIOS:
		return "swift"
	default:
		return NanolinterCoreFlavour
	}
}

// NanolinterImage resolves the image the nanolinter lint job runs in. An
// explicit override wins verbatim (so a caller can pin a digest or use a
// mirror); otherwise the flavour matching the project type is used.
func NanolinterImage(override string, pt projecttype.Type) string {
	if image := strings.TrimSpace(override); image != "" {
		return image
	}

	return NanolinterImageRepository + ":" + NanolinterFlavour(pt)
}
