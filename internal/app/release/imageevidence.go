// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// Native publication extends an unsigned manifest only with the exact public
// documents named by its fresh ledger and producer-pinned index BOMs. This is
// admission, not discovery: unrelated files already in dist are never included.
func validateReleaseImageEvidence(ctx releaseFilesCtx, ledgerFile string, files []string) error {
	if err := validateReleaseDistAsset(ctx, ledgerFile); err != nil {
		return err
	}

	root, err := pathsafe.OpenRoot(ctx.distDir)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	relative, err := filepath.Rel(ctx.distDir, ledgerFile)
	if err != nil {
		return err
	}

	ledger, err := root.ReadFile(relative)
	if err != nil {
		return err
	}

	entries, err := imageledger.Parse(ledger)
	if err != nil {
		return err
	}

	want := map[string]bool{}

	for _, entry := range entries {
		if err := collectReleaseImageEvidence(root, entry, want); err != nil {
			return err
		}
	}

	for _, file := range files {
		name := filepath.Base(file)
		if want[name] && filepath.Clean(file) == filepath.Join(ctx.distDir, name) {
			delete(want, name)
		} else if strings.HasPrefix(name, "image-sbom-") || strings.HasPrefix(name, "sbom-") {
			return fmt.Errorf("unplanned release image evidence %s: %w", file, errs.ErrValidation)
		}
	}

	if len(want) != 0 {
		return fmt.Errorf("missing planned release image evidence: %w", errs.ErrValidation)
	}

	return nil
}

// Admit one image's pinned index and referenced inventories, retaining the
// shared filename set so duplicate inventories across images remain invalid.
func collectReleaseImageEvidence(root *os.Root, entry imageledger.Entry, want map[string]bool) error {
	canonical := imageledger.DefaultSBOMPath(entry.ImageKind, entry.Flavor, entry.Role)

	name := strings.TrimPrefix(canonical, "dist/")
	if entry.SBOM != canonical || name == canonical || filepath.Base(name) != name || entry.SBOMSHA256 == "" {
		return fmt.Errorf("release image evidence requires canonical dist paths and producer pins: %w", errs.ErrValidation)
	}

	body, err := root.ReadFile(name)
	if err != nil {
		return err
	}

	if fmt.Sprintf("%x", sha256.Sum256(body)) != entry.SBOMSHA256 {
		return fmt.Errorf("image SBOM %s does not match sbom_sha256: %w", name, errs.ErrValidation)
	}

	want[name] = true
	id := strings.TrimSuffix(strings.TrimPrefix(name, "image-sbom-"), ".cyclonedx.json")

	return sbom.ReadIndexBOMInventories(body, entry.Ref, func(value string) ([]byte, error) {
		public, err := url.Parse(value)
		if err != nil {
			return nil, err
		}

		name := filepath.Base(public.Path)
		if name != "sbom-"+id+"-amd64.json" && name != "sbom-"+id+"-arm64.json" {
			return nil, fmt.Errorf("unplanned native inventory %s: %w", name, errs.ErrValidation)
		}

		if want[name] {
			return nil, fmt.Errorf("duplicate native inventory %s: %w", name, errs.ErrValidation)
		}

		want[name] = true

		return root.ReadFile(name)
	})
}
