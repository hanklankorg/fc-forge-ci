// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	"github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// maxSBOMBundleSize bounds the SBOMs read back from a hand-off's bundle.
const maxSBOMBundleSize int64 = 1 << 30

// completeSBOMBundle writes the admitted image SBOMs into the one SBOM bundle
// of the release, beside the SBOMs of the build, and rewrites the bundle's
// line in the checksums file. names are files directly in the dist directory.
//
// It runs on an unsigned hand-off only, after the image stage and before the
// provenance statement and every signature, so the bundle is final before
// anything that covers it exists. A hand-off without an SBOM has no bundle
// yet: it is written here, named by naming, and joins the manifest.
//
// The bundle is rebuilt from its members in name order, so it has the bytes
// a hand-off that declared all of these SBOMs at once would have had.
func completeSBOMBundle(ctx releaseFilesCtx, manifest *FileManifest, names []string, naming SBOMBundleNaming) error {
	if len(names) == 0 {
		return nil
	}

	bundle, members, err := handoffSBOMBundle(ctx, manifest, naming)
	if err != nil {
		return err
	}

	created := len(manifest.SBOMs) == 0
	bundled := make(map[string]bool, len(members)+len(names))

	for _, member := range members {
		bundled[member.Name] = true
	}

	for _, name := range names {
		if bundled[name] {
			return fmt.Errorf("image SBOM %s has the name of an SBOM already in %s: %w", name, bundle.Name, errs.ErrValidation)
		}

		body, err := readReleaseFile(filepath.Join(ctx.distDir, name))
		if err != nil {
			return fmt.Errorf("image SBOM: %w", err)
		}

		bundled[name] = true

		members = append(members, archive.TarFile{Name: name, Body: body})
	}

	slices.SortFunc(members, func(a, b archive.TarFile) int { return strings.Compare(a.Name, b.Name) })

	if _, err := writeBundleAtomic(bundle.Path, func(writer *archive.TarGzipWriter) (int, error) {
		for _, member := range members {
			if err := writer.AddBytes(member.Name, member.Body); err != nil {
				return 0, err
			}
		}

		return len(members), nil
	}); err != nil {
		return err
	}

	if err := writeBundleChecksumLine(manifest.Checksums[0].Path, bundle); err != nil {
		return err
	}

	if created {
		manifest.Assets = append(manifest.Assets, bundle)
		manifest.SBOMs = []FileEntry{bundle}
	}

	return nil
}

// handoffSBOMBundle returns the hand-off's SBOM bundle and its members, or
// the bundle to write when the hand-off has no SBOM. An existing bundle must
// be the file its checksums line records: the line is rewritten afterwards,
// and a hand-off whose bundle and checksums disagree is refused here as the
// provenance step would have refused it.
func handoffSBOMBundle(ctx releaseFilesCtx, manifest *FileManifest, naming SBOMBundleNaming) (FileEntry, []archive.TarFile, error) {
	switch len(manifest.SBOMs) {
	case 0:
		project, ver := strings.TrimSpace(naming.ProjectName), version.StripVPrefix(strings.TrimSpace(naming.Version))
		if project == "" || ver == "" {
			return FileEntry{}, nil, fmt.Errorf("the hand-off has no SBOM bundle for the image SBOMs; it is named <project>-<version>-sboms.tar.gz: pass --project-name and --version: %w", errs.ErrUsage)
		}

		name := sbom.BundleName(project, ver)
		bundle := FileEntry{Path: filepath.Join(ctx.distDir, name), Name: name, Source: sbomBundleSource}

		if err := requireNoChecksumLine(manifest.Checksums[0].Path, name); err != nil {
			return FileEntry{}, nil, err
		}

		return bundle, nil, requireAbsentOutput(bundle.Path)
	case 1:
		bundle := manifest.SBOMs[0]
		if !strings.HasSuffix(bundle.Name, "-sboms.tar.gz") {
			return FileEntry{}, nil, fmt.Errorf("the hand-off's SBOM %s is not an SBOM bundle: %w", bundle.Path, errs.ErrValidation)
		}

		if err := requireChecksumLine(manifest.Checksums[0].Path, bundle); err != nil {
			return FileEntry{}, nil, err
		}

		members, err := archive.ReadTarGzipFiles(bundle.Path, maxSBOMBundleSize)
		if err != nil {
			return FileEntry{}, nil, fmt.Errorf("read SBOM bundle %s: %w", bundle.Path, err)
		}

		return bundle, members, nil
	default:
		return FileEntry{}, nil, fmt.Errorf("the hand-off must have one SBOM bundle, not %d SBOM files: %w", len(manifest.SBOMs), errs.ErrValidation)
	}
}

func readChecksumSubjects(checksumsFile string) ([]provenance.Subject, error) {
	body, err := readReleaseFile(checksumsFile)
	if err != nil {
		return nil, fmt.Errorf("checksum file: %w", err)
	}

	return provenance.ParseChecksums(bytes.NewReader(body))
}

// requireChecksumLine holds the bundle to the one line the checksums file has
// for it.
func requireChecksumLine(checksumsFile string, bundle FileEntry) error {
	subjects, err := readChecksumSubjects(checksumsFile)
	if err != nil {
		return err
	}

	digest, err := sha256File(bundle.Path)
	if err != nil {
		return err
	}

	lines := 0

	for _, subject := range subjects {
		if subject.Name != bundle.Name {
			continue
		}

		if subject.SHA256 != digest {
			return fmt.Errorf("SBOM bundle %s does not match its line in %s: %w", bundle.Path, checksumsFile, errs.ErrValidation)
		}

		lines++
	}

	if lines != 1 {
		return fmt.Errorf("%s must list the SBOM bundle %s once, found %d lines: %w", checksumsFile, bundle.Name, lines, errs.ErrValidation)
	}

	return nil
}

func requireNoChecksumLine(checksumsFile, name string) error {
	subjects, err := readChecksumSubjects(checksumsFile)
	if err != nil {
		return err
	}

	if slices.ContainsFunc(subjects, func(subject provenance.Subject) bool { return subject.Name == name }) {
		return fmt.Errorf("%s lists %s, which the hand-off does not declare: %w", checksumsFile, name, errs.ErrValidation)
	}

	return nil
}

// writeBundleChecksumLine rewrites the checksums file with the completed
// bundle's digest: in place of the bundle's line, or as a new last line. Every
// other line keeps its place and digest.
func writeBundleChecksumLine(checksumsFile string, bundle FileEntry) error {
	subjects, err := readChecksumSubjects(checksumsFile)
	if err != nil {
		return err
	}

	digest, err := sha256File(bundle.Path)
	if err != nil {
		return err
	}

	var lines strings.Builder

	listed := false

	for _, subject := range subjects {
		if subject.Name == bundle.Name {
			subject.SHA256, listed = digest, true
		}

		lines.WriteString(subject.SHA256 + "  " + subject.Name + "\n")
	}

	if !listed {
		lines.WriteString(digest + "  " + bundle.Name + "\n")
	}

	if err := os.WriteFile(checksumsFile, []byte(lines.String()), 0o644); err != nil { //nolint:gosec // G306: a public release file, read above through the link-refusing opener.
		return fmt.Errorf("write checksums file %s: %w", checksumsFile, err)
	}

	return nil
}
