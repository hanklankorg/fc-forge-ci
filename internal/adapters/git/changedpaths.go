// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// PathsDiff returns the plain-text diff of paths between two commits, with the
// literal headers a version-bump check reads whatever the repository's Git
// configuration says: no external diff, no textconv, no renames, fixed prefixes.
func (r *Repo) PathsDiff(ctx context.Context, before, after string, paths []string) (string, error) {
	args := make([]string, 0, 13+len(paths))
	args = append(args, "-c", "core.quotePath=true", "diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-color",
		"--no-renames", "--src-prefix=a/", "--dst-prefix=b/", before, after, "--")

	return r.Output(ctx, append(args, paths...)...)
}

// ChangedPaths lists changed committed paths without rename collapsing or
// external diff drivers. NUL separators preserve whitespace in file names.
func (r *Repo) ChangedPaths(ctx context.Context, before, after string) ([]string, error) {
	output, err := r.Run(ctx, "diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", before, after, "--")
	if err != nil || output == "" {
		return nil, err
	}

	return strings.Split(strings.TrimSuffix(output, "\x00"), "\x00"), nil
}

// FileAt returns the exact bytes and the Git mode (100644 or 100755) of the
// regular file path in commit, and false when the commit has no such path. A
// link or a tree there is refused.
func (r *Repo) FileAt(ctx context.Context, commit, path string) (string, string, bool, error) {
	entry, err := r.Output(ctx, "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return "", "", false, err
	}

	if entry == "" {
		return "", "", false, nil
	}

	// <mode> SP <type> SP <object> TAB <path> NUL
	meta, name, _ := strings.Cut(strings.TrimSuffix(entry, "\x00"), "\t")
	fields := strings.Fields(meta)

	if name != path || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return "", "", false, fmt.Errorf("%s at %s is not one regular file: %w", path, commit, errs.ErrValidation)
	}

	body, err := r.Output(ctx, "cat-file", "blob", fields[2])
	if err != nil {
		return "", "", false, err
	}

	return body, fields[0], true, nil
}
