// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/stretchr/testify/require"
)

func TestReadPinnedIndexBOMInventories(t *testing.T) {
	t.Parallel()
	for _, defect := range []string{"", "malformed", "wrong index", "wrong repository", "missing components", "wrong hash algorithm", "wrong reference type", "unsafe URL", "missing inventory", "changed inventory"} {
		t.Run(defect, func(t *testing.T) {
			t.Parallel()
			ref, raw, parts := indexBOMFixture(t)
			body, err := sbom.ComposeIndexBOM(ref, raw, parts)
			require.NoError(t, err)
			switch defect {
			case "malformed":
				body = []byte("null")
			case "wrong index":
				ref = strings.Replace(ref, "sha256:", "sha256:a", 1)
			case "wrong repository":
				body = []byte(strings.Replace(string(body), `"name": "registry.example/acme/widget"`, `"name": "registry.example/other/image"`, 1))
			case "missing components":
				body = []byte(`{"bomFormat":"CycloneDX"}`)
			case "wrong hash algorithm":
				body = []byte(strings.ReplaceAll(string(body), "SHA-256", "SHA-1"))
			case "wrong reference type":
				body = []byte(strings.ReplaceAll(string(body), `"type": "bom"`, `"type": "other"`))
			case "unsafe URL":
				body = []byte(strings.ReplaceAll(string(body), "https://downloads.example", "https://secret@downloads.example"))
			}
			calls := 0
			err = sbom.ReadIndexBOMInventories(body, ref, func(url string) ([]byte, error) {
				calls++
				if defect == "missing inventory" {
					return nil, errors.New("missing inventory")
				}
				for _, part := range parts {
					if url == part.URL {
						if defect == "changed inventory" {
							return append(append([]byte{}, part.Document...), '\n'), nil
						}
						return part.Document, nil
					}
				}
				t.Fatalf("unplanned inventory URL %s", url)
				return nil, nil
			})
			if defect == "" {
				require.NoError(t, err)
				require.Equal(t, 2, calls)
			} else {
				require.Error(t, err)
				if defect != "missing inventory" && defect != "changed inventory" {
					require.Zero(t, calls)
				}
			}
		})
	}
}
