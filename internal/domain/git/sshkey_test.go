// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
)

func newTestKey(t *testing.T, passphrase string) (string, ssh.PublicKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	block, err := ssh.MarshalPrivateKey(priv, "")
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}

	if err != nil {
		t.Fatal(err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	return string(pem.EncodeToMemory(block)), sshPub
}

// A paste into a forge's secret form can add CRLF line ends or drop the
// final newline; OpenSSH refuses CRLF as "error in libcrypto", so the key is
// parsed and re-encoded canonically.
func TestParseSSHPrivateKey_ReadsAPastedKey(t *testing.T) {
	t.Parallel()

	clean, pub := newTestKey(t, "")
	want := string(ssh.MarshalAuthorizedKey(pub))

	for name, pasted := range map[string]string{
		"clean":            clean,
		"no final newline": strings.TrimSuffix(clean, "\n"),
		"CRLF":             strings.ReplaceAll(clean, "\n", "\r\n"),
		"base64":           base64.StdEncoding.EncodeToString([]byte(clean)),
		"wrapped base64":   "\t" + base64.StdEncoding.EncodeToString([]byte(clean)) + "\r\n",
	} {
		key, err := domaingit.ParseSSHPrivateKey(pasted)
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}

		if string(key.AuthorizedKey) != want {
			t.Errorf("%s: public key = %q, want %q", name, key.AuthorizedKey, want)
		}

		if again, err := domaingit.ParseSSHPrivateKey(string(key.OpenSSH)); err != nil || string(again.AuthorizedKey) != want {
			t.Errorf("%s: re-encoded key does not parse back to the same key: %v", name, err)
		}
	}
}

func TestParseSSHPrivateKey_RefusesWithoutQuotingTheKey(t *testing.T) {
	t.Parallel()

	clean, _ := newTestKey(t, "")
	lines := strings.Split(clean, "\n")
	encrypted, _ := newTestKey(t, "secret")

	for name, tc := range map[string]struct {
		text string
		want string
	}{
		"one line":   {strings.Join(lines, ""), "unreadable"},
		"truncated":  {strings.Join(append(lines[:1], lines[2:]...), "\n"), "unreadable"},
		"public key": {"ssh-ed25519 AAAA comment\n", "unreadable"},
		"encrypted":  {encrypted, "passphrase-protected"},
	} {
		_, err := domaingit.ParseSSHPrivateKey(tc.text)
		if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want a validation error saying %q", name, err, tc.want)
		}

		if err != nil && strings.Contains(err.Error(), lines[1]) {
			t.Errorf("%s: key material reached the error", name)
		}
	}
}

func TestParseHostKeyPin_AcceptsTheSHA256FormsOnly(t *testing.T) {
	t.Parallel()

	_, pub := newTestKey(t, "")
	canonical := ssh.FingerprintSHA256(pub)

	for _, text := range []string{canonical, canonical + "=", " " + canonical + "\n"} {
		pin, err := domaingit.ParseHostKeyPin(text)
		if err != nil {
			t.Errorf("%q: %v", text, err)

			continue
		}

		if !pin.Matches(pub) || pin.String() != canonical {
			t.Errorf("%q: pin %s does not match the key's %s", text, pin, canonical)
		}
	}

	for _, text := range []string{
		"",
		"codefloe.com ED25519 " + canonical,
		"256 " + canonical + " host (ED25519)",
		strings.TrimPrefix(canonical, "SHA256:"),
		canonical[:len(canonical)-1],
		ssh.FingerprintLegacyMD5(pub),
		"MD5:" + ssh.FingerprintLegacyMD5(pub),
	} {
		if _, err := domaingit.ParseHostKeyPin(text); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%q: err = %v, want ErrValidation", text, err)
		}
	}

	_, other := newTestKey(t, "")

	pin, err := domaingit.ParseHostKeyPin(canonical)
	if err != nil || pin.Matches(other) {
		t.Errorf("pin matched another key (err %v)", err)
	}
}
