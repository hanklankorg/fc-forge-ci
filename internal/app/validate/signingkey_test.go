// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	trustedKeyPEM = "-----BEGIN PUBLIC KEY-----\ntrusted-release-key\n-----END PUBLIC KEY-----\n"
	otherKeyPEM   = "-----BEGIN PUBLIC KEY-----\nsome-other-key\n-----END PUBLIC KEY-----\n"
)

// fakeCosign derives whatever public key the test says the private key has.
type fakeCosign struct {
	derived string
	err     error
	keyRef  string
}

func (f *fakeCosign) PublicKey(_ context.Context, keyRef string, out, _ io.Writer) error {
	f.keyRef = keyRef

	if f.err != nil {
		return f.err
	}

	_, err := io.WriteString(out, f.derived)

	return err
}

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

// writeTrustAnchor writes the trusted key and, when digest is non-empty, a
// sha256sum-format sidecar carrying it.
func writeTrustAnchor(t *testing.T, key, digest string) (string, string) {
	t.Helper()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "cosign.pub")

	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatalf("write trusted key: %v", err)
	}

	if digest == "" {
		return keyPath, ""
	}

	digestPath := keyPath + ".sha256"
	if err := os.WriteFile(digestPath, []byte(digest+"  cosign.pub\n"), 0o600); err != nil {
		t.Fatalf("write digest sidecar: %v", err)
	}

	return keyPath, digestPath
}

func run(t *testing.T, cosign *fakeCosign, in appvalidate.SigningKeyInput) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := appvalidate.VerifySigningKey(context.Background(), cosign, &out, in)

	return out.String(), err
}

// The signing key deriving the trusted public key is the whole point.
func TestVerifySigningKeyAcceptsTheTrustedKey(t *testing.T) {
	t.Parallel()

	keyPath, digestPath := writeTrustAnchor(t, trustedKeyPEM, digestOf(trustedKeyPEM))
	cosign := &fakeCosign{derived: trustedKeyPEM}

	out, err := run(t, cosign, appvalidate.SigningKeyInput{
		KeyRef:               "env://COSIGN_KEY",
		TrustedPublicKeyPath: keyPath,
		TrustedDigestPath:    digestPath,
	})
	if err != nil {
		t.Fatalf("trusted key rejected: %v", err)
	}

	if cosign.keyRef != "env://COSIGN_KEY" {
		t.Errorf("derived from %q, want env://COSIGN_KEY", cosign.keyRef)
	}

	if !strings.Contains(out, digestOf(trustedKeyPEM)) {
		t.Errorf("success output does not name the matched digest: %q", out)
	}
}

// The case the check exists for: a real key, correctly formed, that is simply
// not the project's key.
func TestVerifySigningKeyRejectsADifferentKey(t *testing.T) {
	t.Parallel()

	keyPath, _ := writeTrustAnchor(t, trustedKeyPEM, "")

	out, err := run(t, &fakeCosign{derived: otherKeyPEM}, appvalidate.SigningKeyInput{
		KeyRef:               "env://COSIGN_KEY",
		TrustedPublicKeyPath: keyPath,
	})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}

	if out != "" {
		t.Errorf("rejected key still produced output: %q", out)
	}

	// Both digests are named, so an operator can tell which key arrived.
	for _, want := range []string{digestOf(trustedKeyPEM), digestOf(otherKeyPEM)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name digest %s: %v", want, err)
		}
	}
}

// No key material may reach the operator-facing output on either path.
func TestVerifySigningKeyNeverPrintsKeyMaterial(t *testing.T) {
	t.Parallel()

	keyPath, _ := writeTrustAnchor(t, trustedKeyPEM, "")

	okOut, err := run(t, &fakeCosign{derived: trustedKeyPEM}, appvalidate.SigningKeyInput{
		KeyRef: "env://COSIGN_KEY", TrustedPublicKeyPath: keyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	badOut, mismatchErr := run(t, &fakeCosign{derived: otherKeyPEM}, appvalidate.SigningKeyInput{
		KeyRef: "env://COSIGN_KEY", TrustedPublicKeyPath: keyPath,
	})

	for _, text := range []string{okOut, badOut, mismatchErr.Error()} {
		for _, secret := range []string{"trusted-release-key", "some-other-key", "BEGIN PUBLIC KEY"} {
			if strings.Contains(text, secret) {
				t.Errorf("output leaked key material %q: %s", secret, text)
			}
		}
	}
}

// A swapped trust anchor is caught before it becomes the comparison target:
// without this, replacing both the key and the signing secret would pass.
func TestVerifySigningKeyRejectsATamperedTrustAnchor(t *testing.T) {
	t.Parallel()

	keyPath, digestPath := writeTrustAnchor(t, otherKeyPEM, digestOf(trustedKeyPEM))

	_, err := run(t, &fakeCosign{derived: otherKeyPEM}, appvalidate.SigningKeyInput{
		KeyRef:               "env://COSIGN_KEY",
		TrustedPublicKeyPath: keyPath,
		TrustedDigestPath:    digestPath,
	})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation for a trust anchor that lost its digest", err)
	}
}

// A sidecar that cannot be read as a digest is a failure, not a skip.
func TestVerifySigningKeyRejectsAMalformedDigestSidecar(t *testing.T) {
	t.Parallel()

	for name, recorded := range map[string]string{
		"empty":     "",
		"truncated": "abc123",
		"uppercase": strings.ToUpper(digestOf(trustedKeyPEM)),
		"not-hex":   strings.Repeat("z", 64),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			keyPath := filepath.Join(dir, "cosign.pub")
			digestPath := keyPath + ".sha256"

			if err := os.WriteFile(keyPath, []byte(trustedKeyPEM), 0o600); err != nil {
				t.Fatalf("write key: %v", err)
			}

			if err := os.WriteFile(digestPath, []byte(recorded+"\n"), 0o600); err != nil {
				t.Fatalf("write sidecar: %v", err)
			}

			if _, err := run(t, &fakeCosign{derived: trustedKeyPEM}, appvalidate.SigningKeyInput{
				KeyRef:               "env://COSIGN_KEY",
				TrustedPublicKeyPath: keyPath,
				TrustedDigestPath:    digestPath,
			}); !errors.Is(err, errs.ErrValidation) {
				t.Errorf("error = %v, want ErrValidation", err)
			}
		})
	}
}

var errDerivationFailed = errors.New("cosign: key not found")

// A cosign that fails, or returns nothing, must not read as a match.
func TestVerifySigningKeyFailsClosedOnDerivationTrouble(t *testing.T) {
	t.Parallel()

	keyPath, _ := writeTrustAnchor(t, trustedKeyPEM, "")

	derivationFailed := errDerivationFailed
	if _, err := run(t, &fakeCosign{err: derivationFailed}, appvalidate.SigningKeyInput{
		KeyRef: "env://COSIGN_KEY", TrustedPublicKeyPath: keyPath,
	}); !errors.Is(err, derivationFailed) {
		t.Errorf("error = %v, want the derivation failure", err)
	}

	if _, err := run(t, &fakeCosign{derived: ""}, appvalidate.SigningKeyInput{
		KeyRef: "env://COSIGN_KEY", TrustedPublicKeyPath: keyPath,
	}); !errors.Is(err, errs.ErrValidation) {
		t.Errorf("empty derivation: error = %v, want ErrValidation", err)
	}
}

// Missing inputs and an unreadable or empty trust anchor are usage/validation
// failures, never a pass.
func TestVerifySigningKeyRejectsUnusableInputs(t *testing.T) {
	t.Parallel()

	keyPath, _ := writeTrustAnchor(t, trustedKeyPEM, "")
	emptyAnchor, _ := writeTrustAnchor(t, "   \n", "")

	cases := map[string]struct {
		in   appvalidate.SigningKeyInput
		want error
	}{
		"no key":           {appvalidate.SigningKeyInput{TrustedPublicKeyPath: keyPath}, errs.ErrUsage},
		"no trusted key":   {appvalidate.SigningKeyInput{KeyRef: "env://COSIGN_KEY"}, errs.ErrUsage},
		"empty trust file": {appvalidate.SigningKeyInput{KeyRef: "env://COSIGN_KEY", TrustedPublicKeyPath: emptyAnchor}, errs.ErrValidation},
		"missing trust file": {appvalidate.SigningKeyInput{
			KeyRef: "env://COSIGN_KEY", TrustedPublicKeyPath: filepath.Join(t.TempDir(), "absent.pub"),
		}, os.ErrNotExist},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := run(t, &fakeCosign{derived: trustedKeyPEM}, testCase.in); !errors.Is(err, testCase.want) {
				t.Errorf("error = %v, want %v", err, fmt.Sprintf("%v", testCase.want))
			}
		})
	}
}
