// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
)

func TestPromoteExpectedInputsRefusedBeforeLogin(t *testing.T) {
	for _, mode := range []string{"malformed", "null", "empty", "duplicate", "conflict", "bad parent", "environment"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)

			key := []byte("inert public key fixture")
			keyPath := filepath.Join(".", "key.pub")
			require.NoError(t, os.WriteFile(keyPath, key, 0o600))
			t.Setenv("RUNNER_TEMP", dir)
			t.Setenv("BASE_INPUTS_JSON", "")

			id := strings.Repeat("a", 64)
			images, err := json.Marshal([]appbaseimages.BaseImageMetadata{{Flavor: "child", Tag: "forge.example/acme/base:final", Ref: "forge.example/acme/base@sha256:" + id, BaseInputID: id}})
			require.NoError(t, err)

			raw := `[{"flavor":"child","base_input_id":"` + id + `"}]`

			switch mode {
			case "malformed":
				raw = `{`
			case "null":
				raw = `null`
			case "empty", "environment":
				raw = `[]`
			case "duplicate":
				raw = strings.TrimSuffix(raw, "]") + "," + strings.TrimPrefix(raw, "[")
			case "conflict":
				raw = strings.ReplaceAll(raw, id, strings.Repeat("b", 64))
			case "bad parent":
				raw = `[{"flavor":"child","base_input_id":"` + id + `","parent_base":{"ref":"other.example/parent:mutable","input_id":"` + id + `"}}]`
			}

			args := []string{"promote", "--all-images-json", string(images), "--server-url", "https://forge.example", "--repository", "acme/base", "--expected-repository", "forge.example/acme/base", "--expected-workflow", "bases.yml", "--cosign-public-key-path", keyPath, "--cosign-public-key-sha256", fmt.Sprintf("%x", sha256.Sum256(key))}

			if mode == "environment" {
				t.Setenv("BASE_INPUTS_JSON", raw)
			} else {
				args = append(args, "--base-inputs-json", raw)
			}

			err = baseImagesPromoteCmd().Run(t.Context(), args)
			require.ErrorContains(t, err, "base-input")
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "validation must precede registry auth scratch/login")
		})
	}
}
