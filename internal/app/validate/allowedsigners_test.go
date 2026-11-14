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

// realKeyB64 is a real ed25519 SSH public key body (base64 wire format,
// RFC 4253 §6.6). Generated with ssh-keygen for testing; not a real
// credential.
const realKeyB64 = "AAAAC3NzaC1lZDI1NTE5AAAAIFKw6+NIKWOtsi2O+WmFbxgYTmak1rpcEPrvx23EQ3yJ" // gitleaks:allow — throwaway test key

// invalidKeyB64 is valid base64 but NOT a valid SSH public key wire
// blob — decodes to bytes that ssh.ParsePublicKey rejects.
const invalidKeyB64 = "dGVzdA=="

func TestValidateAllowedSignersFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr int // number of malformed entries reported
	}{
		{
			name:    "valid single entry",
			content: "release-signer@example.com ssh-ed25519 " + realKeyB64 + "\n",
			wantErr: 0,
		},
		{
			name:    "certificate type with plain key",
			content: "release-signer@example.com ssh-ed25519-cert-v01@openssh.com " + realKeyB64 + "\n",
			wantErr: 1,
		},
		{
			name:    "security key type with plain key",
			content: "release-signer@example.com sk-ssh-ed25519@openssh.com " + realKeyB64 + "\n",
			wantErr: 1,
		},
		{
			name:    "valid multiple principals",
			content: "release-signer@example.com,backup-signer@example.com ssh-ed25519 " + realKeyB64 + "\n",
			wantErr: 0,
		},
		{
			name:    "valid wildcard principal",
			content: "*@example.com ssh-ed25519 " + realKeyB64 + "\n",
			wantErr: 0,
		},
		{
			name:    "valid multiple entries with comments and blanks",
			content: "# comment\n\nrelease-signer@example.com ssh-ed25519 " + realKeyB64 + "\nbackup-signer@example.com ssh-ed25519 " + realKeyB64 + " backup signer's key\n",
			wantErr: 0,
		},
		{
			name:    "valid entry with options",
			content: `release-signer@example.com valid-before="20270101Z" ssh-ed25519 ` + realKeyB64 + "\n",
			wantErr: 0,
		},
		{
			name:    "valid entry with cert-authority option",
			content: "*@example.com cert-authority ssh-ed25519 " + realKeyB64 + "\n",
			wantErr: 0,
		},
		{
			name:    "valid entry with namespaces option",
			content: `release-signer@example.com namespaces="file,git" ssh-ed25519 ` + realKeyB64 + "\n",
			wantErr: 0,
		},
		{
			name:    "declared RSA with Ed25519 key",
			content: "release-signer@example.com ssh-rsa " + realKeyB64,
			wantErr: 1,
		},
		{
			name:    "unknown option",
			content: "release-signer@example.com nonsense-option ssh-ed25519 " + realKeyB64,
			wantErr: 1,
		},
		{
			name:    "multiple option fields",
			content: "release-signer@example.com cert-authority nonsense-option ssh-ed25519 " + realKeyB64,
			wantErr: 1,
		},
		{
			name:    "unterminated quoted option",
			content: "release-signer@example.com namespaces=\"git ssh-ed25519 " + realKeyB64,
			wantErr: 1,
		},
		{
			name:    "case insensitive option names",
			content: `release-signer@example.com CERT-AUTHORITY,NameSpaces="git",VALID-BEFORE="20270101Z" ssh-ed25519 ` + realKeyB64,
		},
		{
			name:    "combined options",
			content: `release-signer@example.com cert-authority,namespaces="file,git",valid-before="20270101Z" ssh-ed25519 ` + realKeyB64,
		},
		{
			name:    "option without required value",
			content: "release-signer@example.com namespaces ssh-ed25519 " + realKeyB64,
			wantErr: 1,
		},
		{
			name:    "missing principal — fields shifted right",
			content: "ssh-ed25519 " + realKeyB64 + " fixture@example.invalid\n",
			wantErr: 1,
		},
		{
			name:    "too few fields",
			content: "release-signer@example.com ssh-ed25519\n",
			wantErr: 1,
		},
		{
			name:    "unknown key type",
			content: "release-signer@example.com foo-bar " + realKeyB64 + "\n",
			wantErr: 1,
		},
		{
			name:    "invalid base64 key",
			content: "release-signer@example.com ssh-ed25519 !!!not-base64!!!\n",
			wantErr: 1,
		},
		{
			name:    "valid base64 but not an SSH key",
			content: "release-signer@example.com ssh-ed25519 " + invalidKeyB64 + "\n",
			wantErr: 1,
		},
		{
			name:    "multiple errors across lines",
			content: "ssh-ed25519 " + realKeyB64 + " release-signer@example.com\nrelease-signer@example.com foo-bar " + realKeyB64 + "\n",
			wantErr: 2,
		},
		{
			name:    "empty file",
			content: "",
			wantErr: 0,
		},
		{
			name:    "only comments",
			content: "# just a comment\n# another\n",
			wantErr: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "allowed_signers")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			err := validate.AllowedSignersFormat(path)
			if (err != nil) != (tt.wantErr != 0) {
				t.Fatalf("error = %v, want malformed = %t", err, tt.wantErr != 0)
			}

			if err != nil && !errors.Is(err, errs.ErrMalformedInput) {
				t.Errorf("expected malformed input, got %v", err)
			}

			if err != nil && strings.Count(err.Error(), path+":") != tt.wantErr {
				t.Errorf("expected %d entry diagnostics, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateAllowedSignersFormat_MissingPrincipalMessage(t *testing.T) {
	t.Parallel()

	// A public-key export is not an allowed-signers entry: it needs a principal.
	path := filepath.Join(t.TempDir(), "allowed_signers")
	content := "ssh-ed25519 " + realKeyB64 + " fixture@example.invalid\n"

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	err := validate.AllowedSignersFormat(path)
	if err == nil {
		t.Fatal("missing principal accepted")
	}

	if !strings.Contains(err.Error(), "principal [options] key-type base64-key") {
		t.Errorf("error should explain the expected entry structure, got: %v", err)
	}
}

func TestValidateAllowedSignersFormat_FileNotFound(t *testing.T) {
	t.Parallel()

	err := validate.AllowedSignersFormat(filepath.Join(t.TempDir(), "nonexistent"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected missing file cause, got %v", err)
	}
}
