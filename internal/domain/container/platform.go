// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The two CPU architectures this pipeline builds and signs images for.
const (
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

// Platform is an OCI image platform: os/arch with an optional variant
// (e.g. linux/arm/v7). This is deliberately NOT the Go build target pair
// (domain/build.SplitPlatform) — OCI platforms carry a variant component
// Go targets don't have. Callers with a stricter contract (image evidence
// scans take exactly os/arch) layer their own rejection on top, the same
// way ecosystem checks layer on SplitPlatform.
type Platform struct {
	OS      string
	Arch    string
	Variant string
}

// ParsePlatform parses "os/arch" or "os/arch/variant"; the format rule for
// OCI platforms lives here and nowhere else.
func ParsePlatform(platform string) (Platform, error) {
	parts := strings.Split(platform, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return Platform{}, fmt.Errorf("platform must be os/arch or os/arch/variant: %s: %w", platform, errs.ErrUsage)
	}

	parsed := Platform{OS: parts[0], Arch: parts[1]}
	if len(parts) == 3 {
		parsed.Variant = parts[2]
	}

	return parsed, nil
}

// RequireNativeArch refuses an image build whose target architecture is not
// the executing host's. Buildah would run the image's RUN steps under CPU
// emulation; each architecture builds on a runner of its own architecture.
func RequireNativeArch(target, host string) error {
	if target != host {
		return fmt.Errorf("image architecture %s must be built on a native %s host, not on %q; route this job to a runner of that architecture: %w",
			target, target, host, errs.ErrValidation)
	}

	return nil
}

// SelectPlatforms narrows the platforms a repository declares to the ones a
// run asks for: selection is RELEASE_PLATFORMS, a comma-separated subset such
// as "linux/amd64". Empty selects every declared platform. An entry with
// surrounding space, another shape than os/arch, a duplicate or a platform the
// repository does not declare is refused before anything is built, naming the
// declared set. The result keeps the declared order.
func SelectPlatforms(declared []string, selection string) ([]string, error) {
	if selection == "" {
		return slices.Clone(declared), nil
	}

	wanted := strings.Split(selection, ",")

	for i, platform := range wanted {
		if strings.TrimSpace(platform) != platform || strings.Count(platform, "/") != 1 || slices.Contains(wanted[:i], platform) {
			return nil, fmt.Errorf("RELEASE_PLATFORMS entry %q is malformed or duplicated; declared: %s: %w",
				platform, strings.Join(declared, ","), errs.ErrUsage)
		}

		if !slices.Contains(declared, platform) {
			return nil, fmt.Errorf("RELEASE_PLATFORMS entry %q is not declared; declared: %s: %w",
				platform, strings.Join(declared, ","), errs.ErrUsage)
		}
	}

	return slices.DeleteFunc(slices.Clone(declared), func(platform string) bool { return !slices.Contains(wanted, platform) }), nil
}
