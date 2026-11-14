// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gpg_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/gpg"
	"github.com/diggsweden/forge-ci/internal/domain/keymaterial"
)

func FuzzDecodeKey(f *testing.F) {
	seeds := []string{
		"",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----\nbody\n-----END PGP PRIVATE KEY BLOCK-----",
		base64.StdEncoding.EncodeToString([]byte("-----BEGIN PGP PRIVATE KEY BLOCK-----\nbody\n-----END PGP PRIVATE KEY BLOCK-----")),
		"not valid base64 ===///===",
		"Zm9v",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		out, err := keymaterial.Key(input)
		if err != nil {
			return
		}

		if strings.HasPrefix(strings.TrimLeft(input, " \t\r\n"), "-----") && string(out) != input {
			t.Fatalf("armored input modified: %q -> %q", input, out)
		}
	})
}

func FuzzParseKeygrips(f *testing.F) {
	for _, seed := range []string{
		sampleColons,
		`grp:::::::::FEDCBA0987654321FEDCBA0987654321FEDCBA09:`,
		// A grp line whose keygrip field is empty: the seed that makes the
		// no-empty-grip property run in an ordinary test run.
		"grp:::::::::\n",
		"",
		"not:colons:format",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		for _, grip := range gpg.ParseKeygrips(input) {
			if grip == "" {
				t.Fatalf("empty keygrip parsed from %q", input)
			}
		}
	})
}
