// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"context"
	"strings"
)

// ChangedPaths lists changed committed paths without rename collapsing or
// external diff drivers. NUL separators preserve whitespace in file names.
func (r *Repo) ChangedPaths(ctx context.Context, before, after string) ([]string, error) {
	output, err := r.Run(ctx, "diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", before, after, "--")
	if err != nil || output == "" {
		return nil, err
	}

	return strings.Split(strings.TrimSuffix(output, "\x00"), "\x00"), nil
}
