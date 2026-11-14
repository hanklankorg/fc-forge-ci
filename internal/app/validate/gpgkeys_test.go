// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// realArmorBlock is a real GPG armored public key block (ed25519,
// generated with gpg --batch --gen-key for testing; not a real
// credential).
const realArmorBlock = `-----BEGIN PGP PUBLIC KEY BLOCK-----

mDMEatoqQhYJKwYBBAHaRw8BAQdA6J7V7z1+yuqChs73KDn8BIltMjNxBpRABQXC
3zs77T60IFRlc3QgT25seSA8dGVzdEBpbnZhbGlkLmV4YW1wbGU+iJAEExYKADgW
IQQe8FYTZb8zfMG+P3EKGLsvKLyF+AUCatoqQgIbIwULCQgHAgYVCgkICwIEFgID
AQIeAQIXgAAKCRAKGLsvKLyF+MEeAP986g5tE4yd2dHnGbuArWAEKt4xCuyrFJr5
NzCxpoGdRAEApxaVECJ9f4HppYsGU0o62pqjiYCF3aJoX8L81zmJGgI=
=513Q
-----END PGP PUBLIC KEY BLOCK-----
`

func TestValidateGPGKeysFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr int
	}{
		{
			name:    "valid single block",
			content: realArmorBlock,
			wantErr: 0,
		},
		{
			name:    "valid multiple blocks",
			content: realArmorBlock + "\n" + realArmorBlock,
			wantErr: 0,
		},
		{
			name:    "empty public key block",
			content: "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n-----END PGP PUBLIC KEY BLOCK-----\n",
			wantErr: 1,
		},
		{
			name:    "base64 text is not a public key",
			content: "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\ndGVzdA==\n-----END PGP PUBLIC KEY BLOCK-----\n",
			wantErr: 1,
		},
		{
			name:    "later block is malformed",
			content: realArmorBlock + "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\ndGVzdA==\n-----END PGP PUBLIC KEY BLOCK-----\n",
			wantErr: 1,
		},
		{
			name:    "later block is truncated",
			content: realArmorBlock + "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\ndGVzdA==\n",
			wantErr: 1,
		},
		{
			name:    "private key envelope",
			content: strings.ReplaceAll(realArmorBlock, "PUBLIC KEY", "PRIVATE KEY"),
			wantErr: 1,
		},
		{
			name: "nested begin marker",
			content: strings.Replace(realArmorBlock, "-----END PGP PUBLIC KEY BLOCK-----",
				"-----BEGIN PGP PUBLIC KEY BLOCK-----\n-----END PGP PUBLIC KEY BLOCK-----", 1),
			wantErr: 1,
		},
		{
			name:    "CRLF",
			content: strings.ReplaceAll(realArmorBlock, "\n", "\r\n"),
		},
		{
			name:    "empty file",
			content: "",
			wantErr: 1,
		},
		{
			name:    "only comments no armor",
			content: "# just a comment\n",
			wantErr: 1,
		},
		{
			name:    "truncated — missing end marker",
			content: "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nmDMEatoqQhYJKwYBBAHaRw8BAQdA\n",
			wantErr: 1,
		},
		{
			name:    "wrong block type",
			content: "-----BEGIN PGP SIGNATURE-----\n\ndGVzdA==\n=dGVzdA==\n-----END PGP SIGNATURE-----\n",
			wantErr: 1,
		},
		{
			name:    "corrupted base64 body",
			content: "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n!!!not-base64!!!\n=dGVzdA==\n-----END PGP PUBLIC KEY BLOCK-----\n",
			wantErr: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "allowed_gpg_keys.asc")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			err := validate.GPGKeysFormat(path)
			if (err != nil) != (tt.wantErr != 0) {
				t.Fatalf("error = %v, want malformed = %t", err, tt.wantErr != 0)
			}

			if err != nil && !errors.Is(err, errs.ErrMalformedInput) {
				t.Errorf("expected malformed input, got %v", err)
			}
		})
	}
}

func TestValidateGPGKeysFormat_FileNotFound(t *testing.T) {
	t.Parallel()

	err := validate.GPGKeysFormat(filepath.Join(t.TempDir(), "nonexistent"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected missing file cause, got %v", err)
	}
}
