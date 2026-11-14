// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Exercise the maintained exporter, not a second fixture-copy implementation.
// Its source location may change; consumer contents and checkout behavior may not.
func TestDemoCheckoutPreservesConsumer(t *testing.T) {
	root := reporoot.Path(t)
	sources, err := filepath.Glob(filepath.Join(root, "test", "live", "fixtures", "consumers", "*", "consumer"))
	require.NoError(t, err)
	require.NotEmpty(t, sources)

	for _, source := range sources {
		name := filepath.Base(filepath.Dir(source))
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "consumer")

			ctx, cancel := context.WithTimeout(t.Context(), liveCommandBound)
			defer cancel()

			command := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts", "ci", "demo-checkout.sh"), name, destination)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)

			expected := consumerFiles(t, source)
			actual := consumerFiles(t, destination)
			require.Equal(t, expected, actual)

			for _, check := range []struct {
				args []string
				want string
			}{
				{[]string{"rev-list", "--count", "HEAD"}, "1\n"},
				{[]string{"status", "--porcelain=v1"}, ""},
			} {
				command := exec.CommandContext(ctx, "git", append([]string{"-C", destination}, check.args...)...)
				output, err := command.CombinedOutput()
				require.NoError(t, err, "%s", output)
				require.Equal(t, check.want, string(output))
			}
		})
	}
}

func TestDemoCheckoutRefusesExistingContents(t *testing.T) {
	root := reporoot.Path(t)
	destination := t.TempDir()
	sentinel := filepath.Join(destination, "keep")
	require.NoError(t, os.WriteFile(sentinel, []byte("original"), 0o600))

	ctx, cancel := context.WithTimeout(t.Context(), liveCommandBound)
	defer cancel()

	command := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts", "ci", "demo-checkout.sh"), "hello-maven-service", destination)
	output, err := command.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "exists and is not empty")
	files := consumerFiles(t, destination)
	require.Equal(t, map[string]consumerFile{"keep": {Body: "original"}}, files)
}

type consumerFile struct {
	Body       string
	Executable bool
}

func consumerFiles(t *testing.T, root string) map[string]consumerFile {
	t.Helper()

	files := make(map[string]consumerFile)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}

			return nil
		}

		info, err := entry.Info()
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular(), "nonregular consumer member: %s", path)
		relative, err := filepath.Rel(root, path)
		require.NoError(t, err)
		body, err := os.ReadFile(path)
		require.NoError(t, err)

		files[filepath.ToSlash(relative)] = consumerFile{Body: string(body), Executable: info.Mode()&0o111 != 0}

		return nil
	})
	require.NoError(t, err)

	return files
}
