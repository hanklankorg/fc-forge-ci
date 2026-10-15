// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/release"
)

// fsOps is the slice of filesystem queries the use case needs. Tests
// substitute an in-memory implementation so the release-notes gate can be
// exercised without touching the disk.
type fsOps interface {
	// FileNonEmpty reports whether path exists AND has size > 0.
	FileNonEmpty(path string) bool
}

// CreateReleaseInput drives `release publish --strategy=recreate`.
type CreateReleaseInput struct {
	Tag              string
	Repository       string
	ReleaseName      string // defaults to Tag
	Draft            bool
	MakeLatest       string
	ReleaseNotesFile string // default release.DefaultReleaseNotesFile
	AssemblyFile     string // required: the staged release assembly to upload exactly
}

// CreateRelease delegates to the ReleaseCreator with exactly the assets the
// release assembly lists, each followed by its signature sidecars. The local
// provider does not satisfy ReleaseCreator — the CLI gates on platform before
// reaching this use case.
func CreateRelease(
	ctx context.Context,
	prov provider.ReleaseCreator,
	fs fsOps,
	out io.Writer,
	in CreateReleaseInput,
) error {
	if in.Tag == "" {
		return fmt.Errorf("tag is required: pass --tag <name> or set $TAG_NAME: %w", errs.ErrUsage)
	}

	if in.Repository == "" {
		return fmt.Errorf("repository is required: pass --repository <owner/repo> or set $REPOSITORY: %w", errs.ErrUsage)
	}

	if in.AssemblyFile == "" {
		return fmt.Errorf("assembly is required: pass --assembly <file> or set $RELEASE_ASSEMBLY: %w", errs.ErrUsage)
	}

	notesFile := cmp.Or(in.ReleaseNotesFile, release.DefaultReleaseNotesFile)
	releaseName := cmp.Or(in.ReleaseName, in.Tag)

	makeLatest, err := normalizeMakeLatest(in.MakeLatest)
	if err != nil {
		return err
	}

	specNotesFile := ""
	if fs.FileNonEmpty(notesFile) {
		specNotesFile = notesFile
	}

	assets, err := collectCreateAssetsFromAssembly(in.AssemblyFile)
	if err != nil {
		return err
	}

	spec := provider.ReleaseSpec{
		Tag:        in.Tag,
		Name:       releaseName,
		NotesFile:  specNotesFile,
		Draft:      in.Draft,
		Prerelease: release.IsPrereleaseTag(in.Tag),
		MakeLatest: makeLatest,
		Assets:     assets,
	}
	_, _ = fmt.Fprintf(out, "Creating release %s with %d asset(s)\n", in.Tag, len(assets))

	return prov.CreateRelease(ctx, in.Repository, spec)
}

//nolint:cyclop // walks the assembly manifest, classifying each entry into one asset bucket — a flat dispatch, not nested logic.
func collectCreateAssetsFromAssembly(path string) ([]string, error) {
	asm, err := readAssembly(path)
	if err != nil {
		return nil, err
	}

	assets := make([]string, 0, len(asm.Assets)+4)
	seen := map[string]string{}
	add := func(path string, required bool) error {
		if path == "" {
			return nil
		}

		if !regularFileExists(path) {
			if required {
				return fmt.Errorf("assembly release asset %q is missing or not a regular file: %w", path, errs.ErrMissingInput)
			}

			return nil
		}

		base := filepath.Base(path)
		if previous, ok := seen[base]; ok {
			if previous == path {
				return nil
			}

			return fmt.Errorf("release asset basename %q is duplicated by %s and %s: %w", base, previous, path, errs.ErrValidation)
		}

		seen[base] = path
		assets = append(assets, path)

		return nil
	}

	addWithSidecars := func(path string, required bool) error {
		if err := add(path, required); err != nil {
			return err
		}

		for _, sidecar := range release.SignatureSidecars(path) {
			if err := add(sidecar, false); err != nil {
				return err
			}
		}

		return nil
	}

	for _, asset := range asm.Assets {
		if err := addWithSidecars(asset.Path, true); err != nil {
			return nil, err
		}
	}

	if asm.SBOMZipFile != "" {
		if err := addWithSidecars(asm.SBOMZipFile, len(asm.SBOMs) > 0); err != nil {
			return nil, err
		}
	}

	if asm.ChecksumFile != "" && regularFileNonEmpty(asm.ChecksumFile) {
		if err := addWithSidecars(asm.ChecksumFile, true); err != nil {
			return nil, err
		}
	}

	return assets, nil
}

func normalizeMakeLatest(value string) (provider.MakeLatestMode, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return provider.MakeLatestTrue, nil
	}

	switch provider.MakeLatestMode(value) {
	case provider.MakeLatestTrue, provider.MakeLatestFalse, provider.MakeLatestLegacy:
		return provider.MakeLatestMode(value), nil
	default:
		return "", fmt.Errorf("make-latest must be one of true, false, legacy (got %q): %w", value, errs.ErrUsage)
	}
}
