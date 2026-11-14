// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cosign

import (
	"bytes"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/keymaterial"
)

func TestEncodedPEMUsesOnlyTheExplicitlySelectedKeyEnvironment(t *testing.T) {
	t.Parallel()

	key := string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED COSIGN PRIVATE KEY", Bytes: []byte("synthetic encrypted bytes")}))
	encoded := base64.StdEncoding.EncodeToString([]byte(key))
	environment := []string{"SELECTED=\t" + encoded + "\r\n", "UNRELATED=" + encoded, "COSIGN_PASSWORD=unchanged"}
	before := slices.Clone(environment)

	got, err := decodedKeyEnv(environment, []string{"sign", "--key", "env://SELECTED"})

	want := []string{"SELECTED=" + key, "UNRELATED=" + encoded, "COSIGN_PASSWORD=unchanged"}
	if err != nil || !slices.Equal(got, want) || !slices.Equal(environment, before) {
		t.Fatal("key conversion changed credential bytes, unrelated authority or the captured source environment")
	}

	for _, reference := range []string{"awskms:///key", "/private/key.pem"} {
		if got, err := decodedKeyEnv(environment, []string{"sign", "--key", reference}); err != nil || !slices.Equal(got, environment) {
			t.Fatal("non-environment key changed captured authority")
		}
	}
}

func TestOversizedCosignEnvironmentKeysRefuseBeforeStartingTheTool(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{
		"raw PEM":       string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED COSIGN PRIVATE KEY", Bytes: bytes.Repeat([]byte("x"), keymaterial.MaxBytes)})),
		"decoded bound": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), keymaterial.MaxBytes+1)),
		"encoded bound": strings.Repeat("x", 2*keymaterial.MaxBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tool := filepath.Join(t.TempDir(), "tool-must-not-start")

			adapter := &Adapter{Bin: tool, Env: []string{"SELECTED=" + value}}
			if _, err := adapter.runCaptured(t.Context(), nil, "public-key", "--key", "env://SELECTED"); !errors.Is(err, keymaterial.ErrSizeLimit) {
				t.Fatalf("size rejection was replaced by subprocess or format failure: %v", err)
			}
		})
	}
}

func TestCosignProcessReceivesDecodedKeyAndLeavesParsingWithCosign(t *testing.T) {
	t.Parallel()

	key := pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED COSIGN PRIVATE KEY", Bytes: []byte("synthetic encrypted bytes")})
	tool := filepath.Join(t.TempDir(), "cosign")

	if err := os.WriteFile(tool, []byte(`#!/bin/sh
case "$SELECTED" in
  '-----BEGIN '*) ;;
  *) exit 31 ;;
esac
test "$UNRELATED" = encoded-unrelated || exit 32
test "$1" = public-key || exit 33
`), 0o700); err != nil {
		t.Fatal(err)
	}

	adapter := &Adapter{Bin: tool, Env: []string{"SELECTED=" + base64.StdEncoding.EncodeToString(key), "UNRELATED=encoded-unrelated"}}
	if _, err := adapter.runCaptured(t.Context(), nil, "public-key", "--key", "env://SELECTED"); err != nil {
		t.Fatalf("cosign process did not receive its decoded key: %v", err)
	}
}
