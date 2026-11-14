// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pathsafe_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
	"github.com/stretchr/testify/require"
)

func TestDirectoryContainmentUsesSafeCanonicalRoots(t *testing.T) {
	fsys := testfs.NewReal(t)
	first := fsys.MkdirAll("source")
	child := fsys.MkdirAll("source", "child")
	sibling := fsys.MkdirAll("source-other")

	for _, pair := range [][2]string{{first, first}, {first, child}, {child, first}} {
		disjoint, err := pathsafe.DisjointRoots(pair[0], pair[1])
		require.NoError(t, err)
		require.False(t, disjoint)
	}

	disjoint, err := pathsafe.DisjointRoots(first, sibling)
	require.NoError(t, err)
	require.True(t, disjoint, "a common textual prefix does not imply containment")

	contained, err := pathsafe.RootContains(fsys.Root, first)
	require.NoError(t, err)
	require.True(t, contained)

	require.NoError(t, os.Symlink(fsys.Root, fsys.Path("alias")))

	disjoint, err = pathsafe.DisjointRoots(first, fsys.Path("alias", "source"))
	if err != nil {
		require.ErrorIs(t, err, errs.ErrValidation)
	}

	require.False(t, disjoint, "an alias must never conceal an overlap")
}

func TestDirectoryContainmentResolvesPermittedSystemAliases(t *testing.T) {
	info, err := os.Lstat("/var/run")
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Skip("host has no /var/run system-layout alias")
	}

	resolved, err := filepath.EvalSymlinks("/var/run")
	require.NoError(t, err)
	contained, err := pathsafe.RootContains("/var/run", resolved)
	require.NoError(t, err)
	require.True(t, contained)

	disjoint, err := pathsafe.DisjointRoots("/var/run", resolved)
	require.NoError(t, err)
	require.False(t, disjoint)
}
