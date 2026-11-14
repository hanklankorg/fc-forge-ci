// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// UploadAttachmentsInput drives UploadAttachments.
type UploadAttachmentsInput struct {
	// Tag is the release tag/name to attach to. Required.
	Tag string
	// Pattern is a comma- or newline-separated list of glob patterns.
	// Spaces within a pattern are literal filename characters. Each pattern
	// is expanded relative to WorkingDir.
	Pattern string
	// WorkingDir is the directory glob expansion is rooted at. Defaults to
	// the current working directory.
	WorkingDir string
}

// UploadAttachments expands Pattern into a list of files and uploads each
// to the release identified by Tag. A file that fails does not stop the
// others: each failure is a warning, and after the last file the function
// fails and names every file the release lacks. A release that holds its
// archive without the signature or provenance beside it is not complete.
// An empty pattern is a no-op.
//
// Path safety: each pattern is required to stay within WorkingDir. Patterns
// that resolve to absolute paths outside WorkingDir or contain `..` segments
// are rejected before any glob expansion runs.
func UploadAttachments(ctx context.Context, up provider.ReleaseAssetUploader, out io.Writer, annot output.Annotator, in UploadAttachmentsInput) error {
	in.Tag = strings.TrimSpace(in.Tag)
	if in.Tag == "" {
		return fmt.Errorf("tag is required: %w", errs.ErrUsage)
	}

	patterns := splitAttachPatterns(in.Pattern)
	if len(patterns) == 0 {
		_, _ = fmt.Fprintln(out, "No attachment patterns configured; nothing to upload.")

		return nil
	}

	root := in.WorkingDir
	if root == "" {
		root = "."
	}

	files, err := expandAttachmentPatterns(root, patterns)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		_, _ = fmt.Fprintf(out, "No files matched patterns %q under %s; nothing to upload.\n", in.Pattern, root)

		return nil
	}

	var failed []string

	// Unavailable only when the network stopped every failed upload: one
	// the forge refused is refused again.
	class := errs.ErrDependencyUnavailable

	for _, file := range files {
		_, _ = fmt.Fprintf(out, "Uploading %s to release...\n", file)

		if err := up.UploadReleaseAsset(ctx, in.Tag, file); err != nil {
			annot.Warningf("Failed to upload %s: %v", file, err)
			failed = append(failed, file)

			if !errs.Retriable(err) && !errs.IsTransport(err) {
				class = errs.ErrValidation
			}
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("%d of %d uploads failed: %s: %w", len(failed), len(files), strings.Join(failed, ", "), class)
	}

	return nil
}

// isRegularFileIn reports whether rel names a regular file inside root.
// Symlinks are not followed and escapes are refused, so a `--clobber` upload
// cannot be pointed at anything outside the workspace.
//
// The check runs through os.Root rather than os.Lstat on the joined path
// because the pattern string is not the only way out of the working
// directory. Rejecting ".." in the pattern stops "../etc/passwd", but a
// symlink already sitting in the workspace does the same job without one:
// with `up -> ..` present, the safe-looking pattern "*/*" expands to files in
// the parent, and each of those is a genuine regular file that an Lstat on
// the joined path is happy to confirm. os.Root refuses to resolve out of the
// directory it was opened at, which is the containment the doc comment above
// claims. Symlinks that stay inside are still skipped, as before.
func isRegularFileIn(root *os.Root, rel string) bool {
	info, err := root.Lstat(rel)
	if err != nil {
		return false
	}

	return info.Mode().IsRegular()
}

// expandAttachmentPatterns expands each glob pattern relative to root and
// returns a sorted, deduplicated list of matching regular files. Patterns
// that reach outside root are rejected.
func expandAttachmentPatterns(root string, patterns []string) ([]string, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open working directory %s: %w: %w", root, err, errs.ErrValidation)
	}

	defer func() { _ = dir.Close() }()

	seen := make(map[string]struct{})

	for _, pattern := range patterns {
		if filepath.IsAbs(pattern) {
			return nil, fmt.Errorf("pattern %q must be relative to the working directory: %w", pattern, errs.ErrValidation)
		}

		if !pathsafe.Relative(pattern) {
			return nil, fmt.Errorf("pattern %q contains \"..\" path segments or unsafe characters: %w", pattern, errs.ErrValidation)
		}

		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w: %w", pattern, err, errs.ErrValidation)
		}

		for _, match := range matches {
			rel, err := filepath.Rel(root, match)
			if err != nil || !isRegularFileIn(dir, rel) {
				continue
			}

			seen[match] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}

	slices.Sort(out)

	return out, nil
}
