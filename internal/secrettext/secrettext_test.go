// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package secrettext_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"unicode"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/safeexec"
	"github.com/diggsweden/forge-ci/internal/secrettext"
)

func TestJWTBoundary_JSONSpellingAndBenignControls(t *testing.T) {
	t.Parallel()

	for _, header := range []string{`{"alg":"HS256"}`, `{ "alg": "HS256" }`, "\n{\n \"kid\":\"owned\",\n \"\\u0061lg\":\"HS256\"\n}", `{"alg":"none"}`} {
		unsigned := base64.RawURLEncoding.EncodeToString([]byte(header)) + ".e30"
		mac := hmac.New(sha256.New, []byte("synthetic-test-only-key"))
		_, _ = mac.Write([]byte(unsigned))

		signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if strings.Contains(header, "none") {
			signature = ""
		}

		token := unsigned + "." + signature
		body := []byte("operation failed: " + token)
		before := bytes.Clone(body)

		redacted := safeexec.RedactKeyMaterial(body)
		if !secrettext.ContainsJWT(body) || !container.UnsafeCosignErrorLine(string(body)) || strings.Contains(string(redacted), token) || !strings.Contains(string(redacted), "JWT-shaped") {
			t.Fatalf("missed header spelling %q", header)
		}

		if !bytes.Equal(before, body) {
			t.Fatal("redaction mutated input")
		}
	}

	for _, body := range []string{"release 1.2.3", "reading foo.bar.baz", "YWJj.e30.c2ln", "e30.e30.c2ln"} {
		if secrettext.ContainsJWT([]byte(body)) || container.UnsafeCosignErrorLine(body) || string(safeexec.RedactKeyMaterial([]byte(body))) != body {
			t.Fatalf("benign text redacted: %s", body)
		}
	}
}

// A secret that reaches a diagnostic with control characters between its bytes
// is still removed. Redacting by exact match alone leaves text that
// reassembles the secret as soon as a reader strips the control characters.
func TestRedact_RemovesASecretSplitByControlCharacters(t *testing.T) {
	t.Parallel()

	const secret = "token-value-DO-NOT-LOG" //nolint:gosec // fixture string standing in for a secret, not a credential.

	for _, test := range []struct {
		name    string
		message string
	}{
		{"verbatim", "server unavailable for token " + secret},
		{"split by a carriage return", "server unavailable for token " + secret[:15] + "\r" + secret[15:]},
		{"split by a control byte", "server unavailable for token " + secret[:15] + "\x01" + secret[15:]},
		{"split in several places", "token " + secret[:5] + "\r" + secret[5:12] + "\x00" + secret[12:]},
		{"wrapped across lines", "token " + secret[:8] + "\n" + secret[8:]},
		{"split by Unicode next line", "token " + secret[:8] + "\u0085" + secret[8:]},
		{"split by Unicode CSI", "token " + secret[:8] + "\u009b" + secret[8:]},
		{"mixed Unicode and ASCII controls", "token " + secret[:5] + "\u0085\r" + secret[5:12] + "\u009f" + secret[12:]},
		{"split by a raw C1 byte", "token " + secret[:8] + "\x85" + secret[8:]},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := secrettext.Redact(test.message, secret)
			if strings.Contains(got, secret) {
				t.Fatalf("redacted text still holds the secret: %q", got)
			}

			if stripped := strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}

				return r
			}, got); strings.Contains(stripped, secret) {
				t.Fatalf("the secret reassembles once control characters are stripped: %q", stripped)
			}

			if !strings.Contains(got, "[redacted]") || !strings.HasPrefix(got, test.message[:5]) {
				t.Fatalf("redaction lost the surrounding diagnostic: %q", got)
			}
		})
	}
}

// Redaction must not eat ordinary text. A secret whose control-stripped form is
// short is removed only where it appears verbatim.
func TestRedact_KeepsOrdinaryTextAndShortSecrets(t *testing.T) {
	t.Parallel()

	if got := secrettext.Redact("dial tcp: connection refused", "token-value-DO-NOT-LOG"); got != "dial tcp: connection refused" {
		t.Errorf("unrelated diagnostic changed: %q", got)
	}

	// A secret whose control-stripped form is "ab" is removed where it
	// appears verbatim, and "abort" survives: matching across control
	// characters would have eaten its first two letters.
	if got := secrettext.Redact("a\rb: abort in a\rb", "a\rb"); strings.Count(got, "[redacted]") != 2 || !strings.Contains(got, "abort") {
		t.Errorf("short secret redaction = %q", got)
	}

	if got := secrettext.Redact("nothing to redact"); got != "nothing to redact" {
		t.Errorf("no secrets changed the message: %q", got)
	}
}

func TestRedact_UnicodeSecretsAndNonControlText(t *testing.T) {
	t.Parallel()

	for _, secret := range []string{"tökén-秘密-value", "token-¡value"} {
		split := strings.ReplaceAll(secret, "-", "-\u0085")
		if got := secrettext.Redact("before "+split+" after", secret); got != "before [redacted] after" {
			t.Fatalf("Unicode secret was not redacted: %q", got)
		}
	}

	for _, separator := range []string{"\u00a0", "é", "\u200d"} {
		message := "before token-" + separator + "value-EXAMPLE after"
		if got := secrettext.Redact(message, "token-value-EXAMPLE"); got != message {
			t.Errorf("non-control text changed: %q", got)
		}
	}
}
