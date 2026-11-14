// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/testutil/sshkey"
)

func newHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()

	_, signer := sshkey.New(t)

	return signer.PublicKey()
}

// known_hosts is written only for a presented key that matches the pin; a
// mismatch or a failed scan leaves no file that a later ssh could trust.
func TestHostPin_InstallsOnlyAMatchingKey(t *testing.T) {
	t.Parallel()

	pinned := newHostKey(t)

	pin, err := domaingit.ParseHostKeyPin(ssh.FingerprintSHA256(pinned))
	require.NoError(t, err)

	for name, scanned := range map[string]struct {
		key ssh.PublicKey
		err error
	}{
		"match":       {key: pinned},
		"mismatch":    {key: newHostKey(t)},
		"scan failed": {err: syscall.ECONNREFUSED},
	} {
		root := t.TempDir()
		scan := func(_ context.Context, host string, port int, keyType string) (ssh.PublicKey, error) {
			require.Equal(t, "host.invalid", host)
			require.Equal(t, 22, port)
			require.Equal(t, "ed25519", keyType)

			return scanned.key, scanned.err
		}

		path, err := pinScannedHostKey(t.Context(), root, "host.invalid", 22, "ed25519", pin, scan)
		if name == "match" {
			require.NoError(t, err)

			body, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, "host.invalid "+string(ssh.MarshalAuthorizedKey(pinned)), string(body))

			continue
		}

		require.Error(t, err, name)
		require.Empty(t, path, name)

		if name == "mismatch" {
			require.ErrorIs(t, err, errs.ErrValidation)
		}

		_, statErr := os.Stat(filepath.Join(root, "known_hosts"))
		require.ErrorIs(t, statErr, os.ErrNotExist, name)
	}
}
