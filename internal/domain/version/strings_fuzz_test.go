// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// FuzzSanitizePathToken exercises the branch-name → path-safe-token
// transform. The function is in the hot path for every snapshot-version
// composition and every SBOM filename; an unexpected panic would take
// out the entire CI run.
//
// Invariants:
//
//   - never panics
//   - output contains only [a-zA-Z0-9._-]
//   - output is valid UTF-8 (input may be invalid)
//   - idempotent on already-clean input
func FuzzSanitizePathToken(f *testing.F) {
	// Seed corpus with shapes we've seen in the wild.
	seeds := []string{
		"",
		"main",
		"feat/awesome",
		"release/2026.05",
		"v1.2.3",
		"v1.2.3-SNAPSHOT",
		"feat/foo bar baz",
		"hotfix/CVE-2024-1234",
		"\x00\x01malformed",
		strings.Repeat("a", 1000),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		got := version.SanitizePathToken(in)

		// Output must be valid UTF-8 even when input is malformed.
		if !utf8.ValidString(got) {
			t.Errorf("output is not valid UTF-8: %q", got)
		}

		// Output must contain only path-safe ASCII.
		for index, char := range got {
			switch {
			case char >= 'a' && char <= 'z':
			case char >= 'A' && char <= 'Z':
			case char >= '0' && char <= '9':
			case char == '.' || char == '_' || char == '-':
			default:
				t.Errorf("byte %d (%U %q) violates [a-zA-Z0-9._-]; full output: %q",
					index, char, char, got)

				return
			}
		}

		// Idempotence: sanitising twice yields the same result.
		got2 := version.SanitizePathToken(got)
		if got != got2 {
			t.Errorf("not idempotent: %q → %q → %q", in, got, got2)
		}
	})
}
