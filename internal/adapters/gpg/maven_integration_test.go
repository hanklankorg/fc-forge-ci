// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build integration

package gpg_test

import (
	"bytes"
	"github.com/diggsweden/forge-ci/internal/adapters/gpg"
	"github.com/diggsweden/forge-ci/internal/pgp"
	"github.com/diggsweden/forge-ci/internal/testutil/gpgkey"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestMavenArmoredSignatureVerifiesWithPublicKey(t *testing.T) {
	key := gpgkey.New(t)
	file := filepath.Join(t.TempDir(), "artifact.jar")
	body := []byte("synthetic publication bytes")
	require.NoError(t, os.WriteFile(file, body, 0o600))
	require.NoError(t, gpg.New().DetachedSignArmored(t.Context(), key.Fingerprint, "", file, file+".asc"))
	signature, err := os.ReadFile(file + ".asc")
	require.NoError(t, err)
	require.Contains(t, string(signature), "BEGIN PGP SIGNATURE")
	require.NoError(t, pgp.VerifyDetachedArmored(bytes.NewReader(body), bytes.NewReader(signature), []byte(key.ArmoredPublicKey())))
}
