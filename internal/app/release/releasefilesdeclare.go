// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	// producerSource labels the files a producer declared itself.
	producerSource = "producer"
	// defaultProducerChecksumsName is the checksums file `release files
	// declare` writes into the dist directory when the caller names none.
	defaultProducerChecksumsName = "checksums.txt"
)

// DeclareReleaseFilesInput drives DeclareReleaseFiles. Assets and SBOMs are
// paths or glob patterns of files under the dist directory.
type DeclareReleaseFilesInput struct {
	FilesInput
	Assets        []string
	SBOMs         []string
	EvidenceFiles []string
	// ChecksumsFile is the checksums file to write. Empty selects
	// checksums.txt in the dist directory.
	ChecksumsFile string
}

// DeclareReleaseFiles writes a producer's unsigned hand-off for a build that
// has no build-tool metadata to import: the checksums file over the declared
// assets and SBOMs, and the release file manifest that names them. It is the
// sibling of ImportGoReleaserFileManifest and refuses the same things: an
// existing manifest, and a dist that already carries what signing writes. It
// also refuses an existing checksums file, because a hand-off has exactly one
// and this one is written here.
func DeclareReleaseFiles(in DeclareReleaseFilesInput) (*FileManifest, error) {
	ctx := releaseFilesContext(in.FilesInput)

	checksumsFile, err := declaredChecksumsPath(ctx, in.ChecksumsFile)
	if err != nil {
		return nil, err
	}

	collector, sboms, err := declaredSubjects(ctx, in)
	if err != nil {
		return nil, err
	}

	// Everything declared so far is a checksum subject. The checksums file
	// and the evidence files join the assets after it is written.
	if writeErr := writeProducerChecksums(checksumsFile, collector.entries); writeErr != nil {
		return nil, writeErr
	}

	manifest, err := declaredFileManifest(ctx, collector, sboms, checksumsFile, in.EvidenceFiles)
	if err != nil {
		// The checksums file is this call's own output; without a manifest
		// it would only block the next attempt.
		return nil, errors.Join(err, os.Remove(checksumsFile))
	}

	return manifest, nil
}

// declaredChecksumsPath returns the checksums file to write, under the dist
// directory, and refuses a hand-off that already has it or a manifest.
func declaredChecksumsPath(ctx releaseFilesCtx, named string) (string, error) {
	checksumsFile := strings.TrimSpace(named)
	if checksumsFile == "" {
		checksumsFile = filepath.Join(ctx.distDir, defaultProducerChecksumsName)
	}

	checksumsFile = filepath.Clean(checksumsFile)
	if err := validateReleaseDistPath(ctx, checksumsFile); err != nil {
		return "", fmt.Errorf("checksums file: %w", err)
	}

	for _, output := range []string{ctx.manifestFile, checksumsFile} {
		if err := requireAbsentOutput(output); err != nil {
			return "", err
		}
	}

	return checksumsFile, nil
}

// declaredSubjects collects the assets and the SBOMs the caller named: the
// files the checksums file covers.
func declaredSubjects(ctx releaseFilesCtx, in DeclareReleaseFilesInput) (*releaseFileCollector, []FileEntry, error) {
	collector := &releaseFileCollector{}

	assets, err := declaredEntries(ctx, "asset", in.Assets)
	if err != nil {
		return nil, nil, err
	}

	if len(assets) == 0 {
		return nil, nil, fmt.Errorf("at least one --asset is required: %w", errs.ErrUsage)
	}

	sboms, err := declaredEntries(ctx, "sbom", in.SBOMs)
	if err != nil {
		return nil, nil, err
	}

	for _, entry := range append(assets, sboms...) {
		if err := collector.add(ctx, entry.Path, entry.Source); err != nil {
			return nil, nil, err
		}
	}

	return collector, sboms, nil
}

// declaredFileManifest completes the collected assets with the checksums file
// and the evidence, and writes the manifest once it passes as unsigned.
func declaredFileManifest(ctx releaseFilesCtx, collector *releaseFileCollector, sboms []FileEntry, checksumsFile string, evidenceFiles []string) (*FileManifest, error) {
	if err := collector.add(ctx, checksumsFile, producerSource); err != nil {
		return nil, err
	}

	evidence, err := addEvidenceEntries(ctx, collector, evidenceFiles)
	if err != nil {
		return nil, err
	}

	manifest := &FileManifest{
		Version:    releaseFilesVersion,
		Assets:     collector.entries,
		Checksums:  []FileEntry{{Path: checksumsFile, Name: filepath.Base(checksumsFile), Source: producerSource}},
		SBOMs:      sboms,
		Evidence:   evidence,
		Provenance: []FileEntry{},
	}

	if err := validateReleaseFileManifest(ctx, manifest); err != nil {
		return nil, err
	}

	if err := validateUnsignedReleaseFiles(ctx, manifest); err != nil {
		return nil, err
	}

	return writeReleaseFileManifest(ctx, manifest, ctx.manifestFile)
}

// requireAbsentOutput refuses a path something already occupies.
func requireAbsentOutput(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("release hand-off file already exists: %s: %w", path, errs.ErrValidation)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	return nil
}

// declaredEntries expands each path or glob pattern to the regular files it
// names, in a stable order. A pattern that names no file is refused: a release
// that silently lacks a declared file is worse than a failed build.
func declaredEntries(ctx releaseFilesCtx, kind string, patterns []string) ([]FileEntry, error) {
	var paths []string

	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return nil, fmt.Errorf("--%s must not be empty: %w", kind, errs.ErrUsage)
		}

		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("--%s pattern %q: %w: %w", kind, pattern, err, errs.ErrUsage)
		}

		files := 0

		for _, match := range matches {
			// A pattern such as dist/* also matches the per-platform
			// directories beside the files; only files are declared.
			if info, statErr := os.Lstat(match); statErr == nil && info.IsDir() {
				continue
			}

			files++

			if !slices.Contains(paths, match) {
				paths = append(paths, match)
			}
		}

		if files == 0 {
			return nil, fmt.Errorf("--%s %q names no file: %w", kind, pattern, errs.ErrMissingInput)
		}
	}

	slices.Sort(paths)

	return entriesFromPaths(ctx, producerSource, paths)
}

// writeProducerChecksums writes the sha256sum-format checksums file over the
// subjects, by name. It creates the file and never replaces one.
func writeProducerChecksums(path string, subjects []FileEntry) error {
	subjects = slices.Clone(subjects)
	slices.SortFunc(subjects, func(a, b FileEntry) int { return strings.Compare(a.Name, b.Name) })

	var lines strings.Builder

	for _, subject := range subjects {
		digest, err := sha256File(subject.Path)
		if err != nil {
			return err
		}

		lines.WriteString(digest + "  " + subject.Name + "\n")
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec,mnd // G302/G304: a public release file at the path the caller named.
	if err != nil {
		return fmt.Errorf("create checksums file %s: %w", path, err)
	}

	if _, err := file.WriteString(lines.String()); err != nil {
		return errors.Join(fmt.Errorf("write checksums file %s: %w", path, err), file.Close(), os.Remove(path))
	}

	if err := file.Close(); err != nil {
		return errors.Join(fmt.Errorf("close checksums file %s: %w", path, err), os.Remove(path))
	}

	return nil
}
