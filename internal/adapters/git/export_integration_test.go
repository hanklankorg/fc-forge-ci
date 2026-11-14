// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build integration

package git_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// ExportAtTag writes exactly the requested paths the tag has, reports the
// ones it lacks, and leaves out what changed after the tag.
func TestExportAtTagWritesOnlyTheTaggedPaths(t *testing.T) {
	t.Parallel()

	repo := isolatedgit.NewRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo.Dir, "examples", "go-cli"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, "examples", "go-cli", "nanolinter.toml"), []byte("tagged\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, "README.md"), []byte("not exported\n"), 0o600))
	repo.Git("add", "--all")
	repo.Git("commit", "-m", "release")
	repo.Git("tag", "v1.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, "examples", "go-cli", "nanolinter.toml"), []byte("after the tag\n"), 0o600))
	repo.Git("commit", "-am", "later")

	dest := t.TempDir()
	missing, err := git.ExportAtTag(t.Context(), "file://"+repo.Dir, "v1.0.0", dest, []string{"examples", ".forge-ci/consumer-defaults.yml"})
	require.NoError(t, err)
	require.Equal(t, []string{".forge-ci/consumer-defaults.yml"}, missing)

	got, err := os.ReadFile(filepath.Join(dest, "examples", "go-cli", "nanolinter.toml"))
	require.NoError(t, err)
	require.Equal(t, "tagged\n", string(got))

	_, err = os.Stat(filepath.Join(dest, "README.md"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
