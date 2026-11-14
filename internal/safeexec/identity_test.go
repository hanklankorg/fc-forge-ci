// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package safeexec_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

func TestExecutableSHA256_IdentifiesBytesWithoutExecuting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	marker := filepath.Join(root, "must-not-exist")
	binary := filepath.Join(root, "builder")
	body := []byte("#!/bin/sh\ntouch '" + marker + "'\nexit 99\n")
	require.NoError(t, os.WriteFile(binary, body, 0o700))

	got, err := safeexec.ExecutableSHA256(binary)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(body)), got)
	require.NoFileExists(t, marker)

	linked := filepath.Join(root, "selected-builder")
	require.NoError(t, os.Symlink(binary, linked))
	linkedDigest, err := safeexec.ExecutableSHA256(linked)
	require.NoError(t, err)
	require.Equal(t, got, linkedDigest)

	require.NoError(t, os.WriteFile(binary, append(body, []byte("# updated binary\n")...), 0o700))

	changed, err := safeexec.ExecutableSHA256(linked)
	require.NoError(t, err)
	require.NotEqual(t, got, changed)
}

func TestExecutableSHA256_RefusesMissingOrNonExecutableInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	data := filepath.Join(root, "not-executable")
	require.NoError(t, os.WriteFile(data, []byte("not an executable"), 0o600))

	for _, path := range []string{filepath.Join(root, "missing"), data, root} {
		got, err := safeexec.ExecutableSHA256(path)
		require.ErrorIs(t, err, errs.ErrPrerequisite)
		require.Empty(t, got)
	}
}
