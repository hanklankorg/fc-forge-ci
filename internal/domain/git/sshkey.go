// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/keymaterial"
)

// SSHPrivateKey is a parsed signing key in the forms ssh and git read.
type SSHPrivateKey struct {
	// OpenSSH is the key re-encoded as an OpenSSH private key file.
	OpenSSH []byte
	// AuthorizedKey is the public key as one authorized_keys line.
	AuthorizedKey []byte
}

// ParseSSHPrivateKey parses an unencrypted private key, armored or standard
// base64-encoded as a forge secret, and re-encodes it canonically. encoding/pem tolerates the CRLF line
// ends a paste can add, which OpenSSH itself refuses as "error in
// libcrypto". Errors never quote the key.
func ParseSSHPrivateKey(text string) (SSHPrivateKey, error) {
	body, err := keymaterial.Key(text)
	if err != nil {
		return SSHPrivateKey{}, fmt.Errorf("SSH private key is unreadable (use whole armored or standard base64 key material): %w: %w", err, errs.ErrValidation)
	}
	defer clear(body)

	raw, err := ssh.ParseRawPrivateKey(body)
	if err != nil {
		if _, ok := errors.AsType[*ssh.PassphraseMissingError](err); ok {
			return SSHPrivateKey{}, fmt.Errorf("SSH private key is passphrase-protected; signing needs an unencrypted key: %w", errs.ErrValidation)
		}

		return SSHPrivateKey{}, fmt.Errorf("SSH private key is unreadable (the secret must be the whole private key, BEGIN and END lines included): %w: %w", err, errs.ErrValidation)
	}

	block, err := ssh.MarshalPrivateKey(raw, "")
	if err != nil {
		return SSHPrivateKey{}, fmt.Errorf("SSH private key: re-encode: %w: %w", err, errs.ErrValidation)
	}

	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return SSHPrivateKey{}, fmt.Errorf("SSH private key: public key: %w: %w", err, errs.ErrValidation)
	}

	return SSHPrivateKey{OpenSSH: pem.EncodeToMemory(block), AuthorizedKey: ssh.MarshalAuthorizedKey(signer.PublicKey())}, nil
}

// HostKeyPin is a host-key fingerprint pin.
type HostKeyPin struct {
	sum []byte
}

// ParseHostKeyPin reads a SHA256 fingerprint in the form ssh-keygen -l
// prints and forges publish, SHA256:<base64>, with or without the base64
// padding. MD5 fingerprints are refused: they no longer bind a key.
func ParseHostKeyPin(text string) (HostKeyPin, error) {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(text), "SHA256:")

	sum, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(encoded, "="))
	if !ok || err != nil || len(sum) != sha256.Size {
		return HostKeyPin{}, fmt.Errorf("host key fingerprint must be the SHA256:<base64> value alone, as ssh-keygen -l prints it, without host or key type: %q: %w", text, errs.ErrValidation)
	}

	return HostKeyPin{sum: sum}, nil
}

// Matches reports whether key carries the pinned fingerprint.
func (p HostKeyPin) Matches(key ssh.PublicKey) bool {
	sum := sha256.Sum256(key.Marshal())

	return bytes.Equal(p.sum, sum[:])
}

// String is the pin in ssh-keygen's canonical form.
func (p HostKeyPin) String() string {
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(p.sum)
}
