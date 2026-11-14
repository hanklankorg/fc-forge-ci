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

// FileAt reads a committed file's exact bytes and mode, says when a commit
// has none, and refuses a link, so a text version check compares real
// content.
func TestFileAtReadsExactCommittedBytes(t *testing.T) {
	t.Parallel()
	adapter, repo := newRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, "gradle.properties"), []byte("version=1.2.3\n\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, "run.xcconfig"), []byte("MARKETING_VERSION = 1.2.3\n"), 0o700))
	require.NoError(t, os.Symlink("gradle.properties", filepath.Join(repo.Dir, "linked.properties")))
	repo.Git("add", "gradle.properties", "run.xcconfig", "linked.properties")
	repo.Git("commit", "-m", "first")
	head := repo.Git("rev-parse", "HEAD")

	body, mode, exists, err := adapter.FileAt(t.Context(), head, "gradle.properties")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "version=1.2.3\n\n", body)
	require.Equal(t, "100644", mode)

	_, mode, _, err = adapter.FileAt(t.Context(), head, "run.xcconfig")
	require.NoError(t, err)
	require.Equal(t, "100755", mode)

	_, _, exists, err = adapter.FileAt(t.Context(), head, "versions.xcconfig")
	require.NoError(t, err)
	require.False(t, exists)

	_, _, _, err = adapter.FileAt(t.Context(), head, "linked.properties")
	require.Error(t, err)
}
