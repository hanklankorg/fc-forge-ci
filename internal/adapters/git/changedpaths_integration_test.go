// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build integration

package git_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/version"
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

// The diff of a version bump, read through the adapter from real git, is the
// exact text the bump check accepts: its final newline kept, its headers
// literal. A diff trimmed like Run's output is refused, so this is the path a
// release with a version bump depends on.
func TestPathsDiffIsTheBumpCheckInput(t *testing.T) {
	t.Parallel()
	adapter, repo := newRepo(t)
	pom := "<project>\n  <artifactId>app</artifactId>\n  <version>1.2.2</version>\n</project>\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, "pom.xml"), []byte(pom), 0o600))
	repo.Git("add", "pom.xml")
	repo.Git("commit", "-m", "first")
	before := repo.Git("rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, "pom.xml"), []byte(strings.Replace(pom, "1.2.2", "1.2.3", 1)), 0o600))
	repo.Git("commit", "-am", "second")
	after := repo.Git("rev-parse", "HEAD")

	patch, err := adapter.PathsDiff(t.Context(), before, after, []string{"pom.xml"})
	require.NoError(t, err)

	files, _ := version.BumpPatchFiles(projecttype.Maven)
	result, err := version.VerifyBumpPatch([]byte(patch), files, "1.2.3")
	require.NoError(t, err)
	require.Equal(t, "1.2.2", result.OldVersion)
}
