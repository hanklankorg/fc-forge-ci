// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestMavenProjectDirectoryRefusesEscapesWithoutChangingCWD(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, os.Mkdir("module", 0o700))
	require.NoError(t, os.Symlink("module", "linked"))

	for _, directory := range []string{"", root, "..", "module/..", "../module", "linked"} {
		_, err := appbuild.MavenProjectDirectory(directory)
		require.Error(t, err, directory)
	}

	directory, err := appbuild.MavenProjectDirectory("module")
	require.NoError(t, err)
	require.Equal(t, "module", directory)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	absolute, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	require.Equal(t, absolute, cwd)
}
