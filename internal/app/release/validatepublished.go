// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// ValidatePublishedInput drives `release validate-published`. The planned
// files are named the way `release publish` was given them: an assembly, or
// explicit assets, or the finalized release file manifest.
type ValidatePublishedInput struct {
	Tag        string
	Repository string
	// Draft says the release was published as a draft and may still be one.
	// It is read back like any other: a forge hides a draft from a token that
	// cannot write, so the caller reads with the token that published it.
	Draft        bool
	AssemblyFile string
	Assets       []string
	Files        FilesInput
}

// ValidatePublishedRelease reads the release of Tag back from the forge and
// compares its files with the ones the release was planned to publish: the
// files `release publish` uploads from the same input, signatures and
// provenance included. A planned file that is absent, or whose size or
// SHA-256 digest differs where the forge reports one, fails the command and
// is named, and so does a
// release that is not visible, draft or not. A file on the
// release that the plan does not name is a warning: `release attachments
// upload` and a consumer's own jobs add files to a release after it is
// published, and no plan lists those.
func ValidatePublishedRelease(ctx context.Context, reader provider.ReleaseReader, out io.Writer, annot output.Annotator, in ValidatePublishedInput) error {
	if reader == nil {
		return fmt.Errorf("release reader is required: %w", errs.ErrUsage)
	}

	tag, repo := strings.TrimSpace(in.Tag), strings.TrimSpace(in.Repository)

	switch {
	case tag == "":
		return fmt.Errorf("tag is required: pass --tag <name> or set $TAG_NAME: %w", errs.ErrUsage)
	case repo == "":
		return fmt.Errorf("repository is required: pass --repository <owner/repo> or set $REPOSITORY: %w", errs.ErrUsage)
	}

	planned, err := plannedReleaseFiles(in)
	if err != nil {
		return err
	}

	release, err := reader.ReadRelease(ctx, repo, tag)

	switch {
	case err != nil:
		return fmt.Errorf("read release %s of %s: %w", tag, repo, err)
	case release == nil && in.Draft:
		return fmt.Errorf("no draft release %s is visible on %s: it was not created, or this token cannot read drafts; read a draft back with the token that published it: %w", tag, repo, errs.ErrValidation)
	case release == nil:
		return fmt.Errorf("no release %s is visible on %s: it was not published, or it is a draft this token cannot read: %w", tag, repo, errs.ErrValidation)
	case release.Draft && !in.Draft:
		return fmt.Errorf("release %s on %s is still a draft: %w", tag, repo, errs.ErrValidation)
	}

	problems, unexpected := comparePublishedFiles(planned, release.Assets)

	for _, name := range unexpected {
		annot.Warningf("Release %s on %s holds %s, which the release plan does not name.", tag, repo, name)
	}

	if len(problems) > 0 {
		return fmt.Errorf("release %s on %s does not hold what was planned: %s: %w", tag, repo, strings.Join(problems, "; "), errs.ErrValidation)
	}

	if len(planned) == 0 {
		_, _ = fmt.Fprintf(out, "Release %s on %s holds no file, as planned\n", tag, repo)
	} else {
		_, _ = fmt.Fprintf(out, "Release %s on %s holds all %d planned files\n", tag, repo, len(planned))
	}

	return nil
}

// plannedFile is a planned release file on disk.
type plannedFile struct {
	path string
	size int64
}

// plannedReleaseFiles maps each planned file's published name to the file.
func plannedReleaseFiles(in ValidatePublishedInput) (map[string]plannedFile, error) {
	paths, err := plannedReleasePaths(in)
	if err != nil {
		return nil, err
	}

	planned := make(map[string]plannedFile, len(paths))

	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("planned release file is missing or not a regular file: %s: %w", path, errs.ErrMissingInput)
		}

		name := filepath.Base(path)
		if _, duplicate := planned[name]; duplicate {
			return nil, fmt.Errorf("duplicate release asset basename: %s: %w", name, errs.ErrValidation)
		}

		planned[name] = plannedFile{path: path, size: info.Size()}
	}

	return planned, nil
}

// plannedReleasePaths resolves the planned files as `release publish` does
// for the same input. An assembly may plan no file: a meta or container-only
// project's release is published from it without one, and reading it back
// still proves the release is visible and not a draft. A manifest or a dist
// that names no file is refused where it is read, as publish refuses it.
func plannedReleasePaths(in ValidatePublishedInput) ([]string, error) {
	switch {
	case in.AssemblyFile != "" && len(in.Assets) > 0:
		return nil, fmt.Errorf("--assembly and --asset both name the planned files; pass one: %w", errs.ErrUsage)
	case in.AssemblyFile != "":
		return collectCreateAssetsFromAssembly(in.AssemblyFile)
	case len(in.Assets) > 0:
		return in.Assets, nil
	default:
		return PublishableReleaseFiles(in.Files)
	}
}

// comparePublishedFiles returns one problem per planned file the release
// lacks or holds at another size or digest, and the names the plan does not
// have, both sorted. A size the forge does not report (GitLab's release
// links) is not compared; a reported size of zero is an empty file. A SHA-256
// digest is compared where the forge reports one (GitHub), so a file of the
// same size with other bytes is found there; a file such a forge gives no
// SHA-256 digest for is a problem, not a file compared by size.
func comparePublishedFiles(planned map[string]plannedFile, published []provider.ListedAsset) ([]string, []string) {
	held := make(map[string]provider.ListedAsset, len(published))

	var unexpected []string

	for _, asset := range published {
		held[asset.Name] = asset

		if _, ok := planned[asset.Name]; !ok {
			unexpected = append(unexpected, asset.Name)
		}
	}

	var problems []string

	for name, file := range planned {
		asset, ok := held[name]

		switch {
		case !ok:
			problems = append(problems, "missing "+name)
		case !asset.SizeUnknown && asset.Size != file.size:
			problems = append(problems, fmt.Sprintf("%s is %d bytes, planned %d", name, asset.Size, file.size))
		case !asset.DigestExpected && asset.Digest == "":
			// The forge reports no digest: name and size are all it offers.
		case !strings.HasPrefix(asset.Digest, "sha256:"):
			// GitHub computes a SHA-256 digest for every file at upload. One
			// without it was not verified, which name and size do not mend.
			problems = append(problems, fmt.Sprintf("%s has no SHA-256 digest on the forge (it reports %q), so its content was not verified", name, asset.Digest))
		default:
			if digest, err := fileSHA256(file.path); err != nil {
				problems = append(problems, fmt.Sprintf("%s could not be read to compare its digest: %v", name, err))
			} else if digest != asset.Digest {
				problems = append(problems, fmt.Sprintf("%s has digest %s on the forge, planned %s", name, asset.Digest, digest))
			}
		}
	}

	slices.Sort(problems)
	slices.Sort(unexpected)

	return problems, unexpected
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path) //nolint:gosec // G304: a file the release plan names, which the caller resolved.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
