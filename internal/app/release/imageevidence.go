// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// ImageSBOMs names the image SBOMs a trusted step admits against the
// release image ledger before they are written into the SBOM bundle.
type ImageSBOMs struct {
	// Ledger is the release image ledger in the dist directory.
	Ledger string
	// Files are the image SBOM files a trusted publisher wrote, each named.
	// They must be exactly the set the ledger pins: its index SBOMs and the
	// platform inventories those name.
	Files []string
	// FromLedger takes the files from the dist directory by the ledger's own
	// pins instead, for a ledger a consumer's jobs handed over. Exclusive
	// with Files.
	FromLedger bool
	// ReleaseTag is the release whose version the names of the files a
	// trusted publisher wrote carry: they are admitted under the names the
	// engine gives a release image's SBOMs.
	ReleaseTag string
}

// admitReleaseImageSBOMs returns the names of the image SBOM files in the dist
// directory that the ledger pins, in name order. This is admission, not
// discovery: every returned file has the bytes the ledger or its index SBOM
// records, and an unrelated file in dist is never included.
func admitReleaseImageSBOMs(ctx releaseFilesCtx, in ImageSBOMs) ([]string, error) {
	if in.FromLedger && len(in.Files) != 0 {
		return nil, fmt.Errorf("name the image SBOMs or take them from the ledger, not both: %w", errs.ErrUsage)
	}

	if err := validateReleaseDistAsset(ctx, in.Ledger); err != nil {
		return nil, err
	}

	root, err := pathsafe.OpenRoot(ctx.distDir)
	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	relative, err := filepath.Rel(ctx.distDir, in.Ledger)
	if err != nil {
		return nil, err
	}

	ledger, err := root.ReadFile(relative)
	if err != nil {
		return nil, err
	}

	entries, err := imageledger.Parse(ledger)
	if err != nil {
		return nil, err
	}

	collect := collectReleaseImageEvidence(in.ReleaseTag)
	if in.FromLedger {
		collect = collectHandoffImageSBOMs(ctx)
	}

	want := map[string]bool{}

	for _, entry := range entries {
		if err := collect(root, entry, want); err != nil {
			return nil, err
		}
	}

	names := slices.Sorted(maps.Keys(want))
	if in.FromLedger {
		return names, nil
	}

	return names, requireNamedImageSBOMs(ctx, in.Files, want)
}

// requireNamedImageSBOMs holds the files a trusted publisher named to exactly
// the set the ledger pins, each directly in the dist directory.
func requireNamedImageSBOMs(ctx releaseFilesCtx, files []string, want map[string]bool) error {
	for _, file := range files {
		name := filepath.Base(file)
		if !want[name] || filepath.Clean(file) != filepath.Join(ctx.distDir, name) {
			return fmt.Errorf("unplanned release image SBOM %s: %w", file, errs.ErrValidation)
		}

		delete(want, name)
	}

	if len(want) != 0 {
		return fmt.Errorf("missing planned release image SBOM: %w", errs.ErrValidation)
	}

	return nil
}

// refuseImageSBOMEvidence keeps an image SBOM from being published as a file
// of its own: with a ledger, the SBOM bundle is where it goes.
func refuseImageSBOMEvidence(files []string) error {
	for _, file := range files {
		if name := filepath.Base(file); strings.HasPrefix(name, "image-sbom-") || strings.HasPrefix(name, "sbom-") ||
			strings.Contains(name, "-"+string(sbom.LayerAnalyzedContainer)+"-") {
			return fmt.Errorf("unplanned release image evidence %s: an image SBOM is published in the SBOM bundle: %w", file, errs.ErrValidation)
		}
	}

	return nil
}

// collectReleaseImageEvidence admits one image's pinned index SBOM and the
// platform inventories it names, under the names the engine gives them,
// retaining the shared filename set so duplicate inventories across images
// remain invalid. The names carry the release commit; one release has one, so
// every image's names must carry the commit the first one's does.
func collectReleaseImageEvidence(releaseTag string) func(*os.Root, imageledger.Entry, map[string]bool) error {
	var commit string

	return func(root *os.Root, entry imageledger.Entry, want map[string]bool) error {
		if commit == "" {
			commit = imageSBOMCommit(entry.SBOM)
		}

		canonical := imageledger.ReleaseImageSBOMPath(entry.Flavor, entry.Role, releaseTag, commit)
		if entry.ImageKind == imageledger.ImageKindBase {
			canonical = ""
		}

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
		inventories := map[string]bool{}

		for _, platform := range []string{"linux/amd64", "linux/arm64"} {
			inventories[imageledger.ReleaseImageSBOMFilename(entry.Flavor, entry.Role, releaseTag, commit, platform)] = true
		}

		return sbom.ReadIndexBOMInventories(body, entry.Ref, func(name string) ([]byte, error) {
			if !inventories[name] {
				return nil, fmt.Errorf("unplanned native inventory %s: %w", name, errs.ErrValidation)
			}

			if want[name] {
				return nil, fmt.Errorf("duplicate native inventory %s: %w", name, errs.ErrValidation)
			}

			want[name] = true

			return root.ReadFile(name)
		})
	}
}

// imageSBOMCommit is the commit segment of an index SBOM path the engine
// names, <name>-<version>-<commit>-analyzed-container-index-sbom.cyclonedx.json.
// Whether it is a commit at all is for the caller's canonical name to decide.
func imageSBOMCommit(path string) string {
	stem, _ := strings.CutSuffix(path, "-"+string(sbom.LayerAnalyzedContainer)+"-"+sbom.ImageSBOMIndexSlot+"-sbom.cyclonedx.json")

	return stem[strings.LastIndex(stem, "-")+1:]
}

// collectHandoffImageSBOMs admits the image SBOMs of a ledger that a
// consumer's jobs handed over. An entry has an SBOM to bundle when the ledger
// pins it and it is a file directly in the dist directory: the pin is what
// binds the file to the entry. An index SBOM brings the inventories it names,
// each bound by the checksum the index SBOM records. An entry without a pin
// has no SBOM before signing, where the signer generates and attests it, and
// a base image's premade SBOM is outside the dist directory; both keep theirs
// as an attestation and are left out here. Any other pinned SBOM outside the
// dist directory is refused: it would be attested but never published.
func collectHandoffImageSBOMs(ctx releaseFilesCtx) func(*os.Root, imageledger.Entry, map[string]bool) error {
	return func(root *os.Root, entry imageledger.Entry, want map[string]bool) error {
		if entry.SBOMSHA256 == "" || entry.ImageKind == imageledger.ImageKindBase {
			return nil
		}

		name := filepath.Base(entry.SBOM)
		if filepath.Clean(entry.SBOM) != filepath.Join(ctx.distDir, name) {
			return fmt.Errorf("pinned image SBOM %s is not in %s, so the SBOM bundle cannot hold it: %w", entry.SBOM, ctx.distDir, errs.ErrValidation)
		}

		body, err := root.ReadFile(name)
		if err != nil {
			return err
		}

		if fmt.Sprintf("%x", sha256.Sum256(body)) != entry.SBOMSHA256 {
			return fmt.Errorf("image SBOM %s does not match sbom_sha256: %w", name, errs.ErrValidation)
		}

		if want[name] {
			return fmt.Errorf("duplicate image SBOM %s: %w", name, errs.ErrValidation)
		}

		want[name] = true

		if _, _, index, err := sbom.IndexBOMInventories(body); err != nil || !index {
			return err
		}

		return sbom.ReadIndexBOMInventories(body, entry.Ref, func(name string) ([]byte, error) {
			if want[name] {
				return nil, fmt.Errorf("duplicate native inventory %s: %w", name, errs.ErrValidation)
			}

			want[name] = true

			return root.ReadFile(name)
		})
	}
}
