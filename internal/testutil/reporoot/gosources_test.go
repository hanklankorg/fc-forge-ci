// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package reporoot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWalkGoSources_SkipsOnlyNonSourceDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, rel := range []string{
		"main.go", "dist/goreleaser.go", "node_modules/pkg/x.go", ".git/hooks/x.go", "vendored/.git/x.go",
		"internal/app/dist/compiled.go", "internal/app/node_modules/compiled.go", "internal/app/vendor/compiled.go",
		"nested/go.mod", "nested/.git/HEAD", "nested/copied.go",
		"checkouts/worktree/go.mod", "checkouts/worktree/.git", "checkouts/worktree/copied.go",
		"examples/standalone/go.mod", "examples/standalone/compiled.go",
		"internal/plain/.git", "internal/plain/compiled.go",
		"malformed/.git", "malformed/go.mod/compiled.go",
	} {
		file := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))

		body := "package p\n"
		if filepath.Base(file) == "go.mod" {
			body = "module example.invalid/nested\n"
		}

		require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
	}

	var seen []string

	visited, err := WalkGoSources(root, func(_, rel string) error {
		seen = append(seen, rel)

		return nil
	})
	require.NoError(t, err)
	require.Equal(t, len(seen), visited)
	require.ElementsMatch(t, []string{
		"main.go", "internal/app/dist/compiled.go", "internal/app/node_modules/compiled.go", "internal/app/vendor/compiled.go",
		"internal/plain/compiled.go", "examples/standalone/compiled.go", "malformed/go.mod/compiled.go",
	}, seen)
}
