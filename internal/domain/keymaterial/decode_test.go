// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package keymaterial_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/keymaterial"
)

func TestStandardBase64PreservesBytesAndSupportedWrapping(t *testing.T) {
	t.Parallel()

	secret := []byte("binary\x00\xff signing material\n")
	encoded := base64.StdEncoding.EncodeToString(secret)
	half := len(encoded) / 2

	for _, value := range []string{encoded, " \t" + encoded[:half] + "\r\n\t" + encoded[half:] + "\n"} {
		body, err := keymaterial.Base64(value)
		if err != nil || !bytes.Equal(body, secret) {
			t.Fatalf("standard decoder changed credential bytes: err=%v", err)
		}
	}
}

func TestCredentialDecodeRefusesEmptyMalformedAndOversizedInputWithoutPartialBytes(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{
		"empty": " \t\r\n", "corrupt": "c2VjcmV0!PRIVATE-CANARY", "unpadded": "YWI",
		"URL alphabet":  base64.URLEncoding.EncodeToString([]byte{0xfb, 0xff}),
		"encoded bound": strings.Repeat("x", 2*keymaterial.MaxBytes+1),
		"decoded bound": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), keymaterial.MaxBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body, err := keymaterial.Base64(value)
			if !errors.Is(err, errs.ErrMalformedInput) || body != nil || strings.Contains(err.Error(), "PRIVATE-CANARY") {
				t.Fatalf("invalid credential returned bytes or exposed its content: len=%d err=%v", len(body), err)
			}
		})
	}
}

func TestKeyEnvelopeLeavesArmoredMaterialUntouchedAndDecodesOnce(t *testing.T) {
	t.Parallel()

	for _, label := range []string{"PGP PRIVATE KEY BLOCK", "OPENSSH PRIVATE KEY", "ENCRYPTED COSIGN PRIVATE KEY"} {
		original := "\r\n  -----BEGIN " + label + "-----\nYnl0ZXM=\n-----END " + label + "-----\n"
		for _, input := range []string{original, base64.StdEncoding.EncodeToString([]byte(original))} {
			decoded, err := keymaterial.Key(input)
			if err != nil || string(decoded) != original {
				t.Fatalf("key envelope changed bytes for %s: %v", label, err)
			}
		}
	}
}
