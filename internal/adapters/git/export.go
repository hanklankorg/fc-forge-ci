// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"context"
	"fmt"
	"os"

	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// ExportAtTag writes the given paths of a remote repository at a tag into
// dest, fetched anonymously with depth 1 into a throwaway repository, and
// returns the paths the tag's tree does not have. Nothing else of the
// remote is written.
func ExportAtTag(ctx context.Context, repoURL, tag, dest string, paths []string) ([]string, error) {
	dir, err := os.MkdirTemp("", "forge-ci-export-")
	if err != nil {
		return nil, err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	repo := &Repo{Dir: dir}
	if _, err = repo.Run(ctx, "init", "--quiet", "--bare"); err != nil {
		return nil, err
	}

	if err = repo.RemoteAdd(ctx, defaultRemote, repoURL); err != nil {
		return nil, err
	}

	ref := refsTagsPrefix + tag
	if err = repo.Fetch(ctx, repoURL, []string{"+" + ref + ":" + ref}, runcontext.Credential{}, 1); err != nil {
		return nil, fmt.Errorf("fetch %s from %s: %w", tag, repoURL, err)
	}

	commit := ref + "^{commit}"

	var present, missing []string

	for _, p := range paths {
		if _, statErr := repo.Run(ctx, "cat-file", "-e", commit+":"+p); statErr != nil {
			missing = append(missing, p)
		} else {
			present = append(present, p)
		}
	}

	if len(present) > 0 {
		args := append([]string{"--work-tree=" + dest, "checkout", commit, "--"}, present...)
		if _, err = repo.Run(ctx, args...); err != nil {
			return nil, err
		}
	}

	return missing, nil
}
