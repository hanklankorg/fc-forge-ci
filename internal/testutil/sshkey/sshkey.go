// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package sshkey generates throwaway SSH keys for tests that parse a signing
// key or present a host key.
package sshkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"

	"golang.org/x/crypto/ssh"
)

// New returns a fresh ed25519 key as an unencrypted OpenSSH private key
// file, with its signer.
func New(tb testing.TB) (string, ssh.Signer) {
	tb.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}

	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		tb.Fatal(err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		tb.Fatal(err)
	}

	return string(pem.EncodeToMemory(block)), signer
}

// PrivateKey returns a fresh unencrypted OpenSSH private key file.
func PrivateKey(tb testing.TB) string {
	tb.Helper()

	key, _ := New(tb)

	return key
}
