// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package secrettext handles diagnostic text only, not credential verification.
package secrettext

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

//nolint:gochecknoglobals // immutable token-shape matcher.
var compactJWS = regexp.MustCompile(`([A-Za-z0-9_-]+)\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)

// ContainsJWT recognizes compact JWS-shaped text with a JSON algorithm header.
// Header spelling/whitespace is irrelevant. Oversized candidates are treated as
// sensitive without decoding; the detector never verifies credentials.
func ContainsJWT(body []byte) bool {
	for match := compactJWS.FindSubmatchIndex(body); match != nil; match = compactJWS.FindSubmatchIndex(body) {
		header := body[match[2]:match[3]]
		if len(header) > 16<<10 {
			return true
		}

		decoded, err := base64.RawURLEncoding.DecodeString(string(header))
		if err == nil {
			var value struct {
				Algorithm string `json:"alg"`
			}
			if json.Unmarshal(decoded, &value) == nil && value.Algorithm != "" {
				return true
			}
		}

		body = body[match[1]:]
	}

	return false
}

// placeholder replaces a redacted secret.
const placeholder = "[redacted]"

// minInterleavedSecret is the shortest control-stripped secret worth matching
// across control characters. Below it the stripped form is short enough to
// occur in ordinary text, and redacting it would hide the diagnostic instead
// of the secret.
const minInterleavedSecret = 8

// Redact removes each secret from diagnostic text.
//
// A secret is also removed when control characters sit between its bytes,
// either because the secret itself holds one or because something split it on
// the way into the message. That matters for what a reader does next: strip
// the control characters from a diagnostic that was redacted by exact match
// alone, and the secret reassembles itself.
func Redact(message string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}

		message = strings.ReplaceAll(message, secret, placeholder)

		if compact := stripControl(secret); len(compact) >= minInterleavedSecret {
			message = redactInterleaved(message, compact)
		}
	}

	return message
}

// RedactError preserves errors.Is classifications without exposing a raw
// secret-bearing cause via errors.As or an unwrap chain.
func RedactError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}

	return &redactedError{cause: err, message: Redact(err.Error(), secrets...)}
}

// stripControl returns value without its control characters.
func stripControl(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}

		return r
	}, value)
}

// redactInterleaved replaces every run of message that equals compact once its
// control characters are removed.
func redactInterleaved(message, compact string) string {
	var out strings.Builder

	for index := 0; index < len(message); {
		width := interleavedMatch(message[index:], compact)
		if width == 0 {
			out.WriteByte(message[index])
			index++

			continue
		}

		out.WriteString(placeholder)

		index += width
	}

	return out.String()
}

// interleavedMatch returns the length of the prefix of text that equals
// compact once control characters are removed, or zero when there is none.
func interleavedMatch(text, compact string) int {
	matched, consumed := 0, 0

	for consumed < len(text) && matched < len(compact) {
		// Encoded controls and ordinary runes can share a leading UTF-8 byte.
		control, width := utf8.DecodeRuneInString(text[consumed:])
		if width > 1 && unicode.IsControl(control) {
			consumed += width

			continue
		}

		char := text[consumed]
		switch {
		case char == compact[matched]:
			matched++
		case !unicode.IsControl(rune(char)):
			return 0
		}

		consumed++
	}

	if matched < len(compact) {
		return 0
	}

	return consumed
}

type redactedError struct {
	cause   error
	message string
}

func (e *redactedError) Error() string        { return e.message }
func (e *redactedError) Is(target error) bool { return errors.Is(e.cause, target) }
