// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build integration

package git_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChangedPathsPreservesNamesAndDoesNotCollapseRenames(t *testing.T) {
	t.Parallel()
	adapter, repo := newRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, "old.md"), []byte("same contents\n"), 0o600))
	repo.Git("add", "old.md")
	repo.Git("commit", "-m", "first")
	before := repo.Git("rev-parse", "HEAD")
	require.NoError(t, os.Rename(filepath.Join(repo.Dir, "old.md"), filepath.Join(repo.Dir, "CHANGELOG.md")))
	require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, "line\nbreak.md"), []byte("another file\n"), 0o600))
	repo.Git("add", "--all")
	repo.Git("commit", "-m", "second")
	after := repo.Git("rev-parse", "HEAD")

	paths, err := adapter.ChangedPaths(t.Context(), before, after)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"old.md", "CHANGELOG.md", "line\nbreak.md"}, paths)
}
