// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"context"
	"strings"
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
