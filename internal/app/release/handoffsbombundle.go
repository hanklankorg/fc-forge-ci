// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// sbomBundleSource labels the SBOM bundle a hand-off's SBOMs are written into.
const sbomBundleSource = "sbom_bundle"

// SBOMBundleNaming names the SBOM bundle of an unsigned hand-off,
// <project>-<version>-sboms.tar.gz. Both are required when the hand-off has
// an SBOM, and unused when it has none.
type SBOMBundleNaming struct {
	ProjectName string
	// Version is the release version or tag; a v prefix is dropped.
	Version string
}

// bundleHandoffSBOMs writes the SBOMs of an unsigned hand-off into the one
// SBOM bundle the release publishes, in the dist directory, and returns its
// entry. The bundle holds each SBOM under its file name, in name order, so the
// same SBOMs give the same bundle. One SBOM is bundled like several: a
// release has the bundle or no SBOM. The SBOM files stay where they are and
// are no longer release files.
func bundleHandoffSBOMs(ctx releaseFilesCtx, sboms []FileEntry, naming SBOMBundleNaming) (FileEntry, error) {
	project, ver := strings.TrimSpace(naming.ProjectName), version.StripVPrefix(strings.TrimSpace(naming.Version))
	if project == "" || ver == "" {
		return FileEntry{}, fmt.Errorf("the SBOM bundle is named <project>-<version>-sboms.tar.gz: pass --project-name and --version: %w", errs.ErrUsage)
	}

	sboms = slices.Clone(sboms)
	slices.SortFunc(sboms, func(a, b FileEntry) int { return strings.Compare(a.Name, b.Name) })

	for i := 1; i < len(sboms); i++ {
		if sboms[i].Name == sboms[i-1].Name {
			return FileEntry{}, fmt.Errorf("duplicate SBOM file name: %s (first: %s, second: %s): %w", sboms[i].Name, sboms[i-1].Path, sboms[i].Path, errs.ErrValidation)
		}
	}

	name := sbom.BundleName(project, ver)
	path := filepath.Join(ctx.distDir, name)

	if err := requireAbsentOutput(path); err != nil {
		return FileEntry{}, err
	}

	if _, err := writeBundleAtomic(path, func(bundle *archive.TarGzipWriter) (int, error) {
		for _, entry := range sboms {
			if err := addToBundle(bundle, entry.Path, entry.Name); err != nil {
				return 0, err
			}
		}

		return len(sboms), nil
	}); err != nil {
		return FileEntry{}, err
	}

	return FileEntry{Path: path, Name: name, Source: sbomBundleSource}, nil
}
