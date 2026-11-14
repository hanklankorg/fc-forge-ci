// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
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

// pinnedNanolinterImage is an image reference pinned by a full digest, the
// pattern the GitLab component's nanolinter-image input admits.
var pinnedNanolinterImage = regexp.MustCompile(`^[^[:space:]]+@sha256:[0-9a-f]{64}$`)

// NanolinterImage admits the image the nanolinter lint job runs in: a
// reference pinned by digest, which the job receives the checkout and the run
// context in. A tag, such as the flavour tag of the project type, names
// whatever the registry serves at the time, so it is refused, with the
// flavour that carries the project type's toolchain as the hint.
func NanolinterImage(image string, pt projecttype.Type) (string, error) {
	image = strings.TrimSpace(image)
	if pinnedNanolinterImage.MatchString(image) {
		return image, nil
	}

	hint := NanolinterImageRepository + ":" + NanolinterFlavour(pt) + "@sha256:<digest>"
	if image == "" {
		return "", fmt.Errorf("lint-engine nanolinter needs nanolinter-image, a nanolinter image pinned by digest, such as %s: %w", hint, errs.ErrUsage)
	}

	return "", fmt.Errorf("nanolinter-image %q is not pinned by digest; pin the reviewed image, such as %s: %w", image, hint, errs.ErrUsage)
}
