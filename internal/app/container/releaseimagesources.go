// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// AdmitReleaseImageLayoutSources preserves native-job identity without copying
// untrusted layout trees into a combined workspace. All roots pass before scans.
func AdmitReleaseImageLayoutSources(sources map[string]string, images []pipeline.ReleaseImage) error {
	for arch := range sources {
		if arch != "amd64" && arch != "arm64" {
			return fmt.Errorf("unsupported layout source architecture %q: %w", arch, errs.ErrUsage)
		}
	}

	for _, arch := range []string{"amd64", "arm64"} {
		var selected []pipeline.ReleaseImage

		for _, image := range images {
			if slices.Contains(nonEmptyLines(image.Archs), arch) {
				image.Archs = arch
				selected = append(selected, image)
			}
		}

		root := sources[arch]
		if root == "" {
			if len(selected) != 0 {
				return fmt.Errorf("missing native %s layout source: %w", arch, errs.ErrMissingInput)
			}

			continue
		}

		if _, err := os.Lstat(root); os.IsNotExist(err) && len(selected) == 0 {
			continue
		}

		if err := AdmitReleaseImageLayouts(root, selected); err != nil {
			return fmt.Errorf("%s layout source: %w", arch, err)
		}
	}

	return nil
}

// ParseReleaseImageLayoutSources rejects ambiguous bindings before file access.
func ParseReleaseImageLayoutSources(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}

	sources := map[string]string{}

	for _, value := range values {
		arch, path, ok := strings.Cut(value, "=")
		if !ok || path == "" || (arch != "amd64" && arch != "arm64") || sources[arch] != "" {
			return nil, fmt.Errorf("layout-source requires unique amd64=PATH or arm64=PATH: %w", errs.ErrUsage)
		}

		sources[arch] = path
	}
	// Each root is one native job's artifact, admitted with only its own
	// architecture's layouts; one root for both would always be refused there.
	if amd, arm := sources["amd64"], sources["arm64"]; amd != "" && arm != "" && filepath.Clean(amd) == filepath.Clean(arm) {
		return nil, fmt.Errorf("layout-source amd64 and arm64 name the same root %q; pass each native job's own root, or --layouts-dir for one merged directory: %w", amd, errs.ErrUsage)
	}

	return sources, nil
}
