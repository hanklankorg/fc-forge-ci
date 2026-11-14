// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
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

	for _, defect := range []string{"", "malformed", "wrong index", "wrong repository", "missing components", "wrong hash algorithm", "wrong reference type", "unsafe name", "missing inventory", "changed inventory"} {
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
			case "unsafe name":
				body = []byte(strings.ReplaceAll(string(body), `"url": "sbom-widget-`, `"url": "https://secret@downloads.example/sbom-widget-`))
			}

			calls := 0

			err = sbom.ReadIndexBOMInventories(body, ref, func(name string) ([]byte, error) {
				calls++

				if defect == "missing inventory" {
					return nil, errors.New("missing inventory")
				}

				for _, part := range parts {
					if name == part.Name {
						if defect == "changed inventory" {
							return append(append([]byte{}, part.Document...), '\n'), nil
						}

						return part.Document, nil
					}
				}

				t.Fatalf("unplanned inventory %s", name)

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

// The signer attests each inventory on the child digest the index BOM names,
// so a document is an index BOM only when every platform is a complete,
// pinned reference inside the index repository.
func TestIndexBOMInventoriesNamesEachChildByDigest(t *testing.T) {
	t.Parallel()

	ref, raw, parts := indexBOMFixture(t)
	body, err := sbom.ComposeIndexBOM(ref, raw, parts)
	require.NoError(t, err)

	gotRef, inventories, ok, err := sbom.IndexBOMInventories(body)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ref, gotRef)
	require.Equal(t, []sbom.IndexBOMInventory{
		{Platform: parts[1].Platform, ImageRef: parts[1].ImageRef, Name: parts[1].Name, SHA256: parts[1].SHA256},
		{Platform: parts[0].Platform, ImageRef: parts[0].ImageRef, Name: parts[0].Name, SHA256: parts[0].SHA256},
	}, inventories)

	_, inventories, ok, err = sbom.IndexBOMInventories(parts[0].Document)
	require.NoError(t, err)
	require.False(t, ok, "the inventory of one image is no index BOM")
	require.Empty(t, inventories)

	for name, change := range map[string][2]string{
		"foreign child":   {`"name": "registry.example/acme/widget@`, `"name": "registry.example/other/image@`},
		"unpinned child":  {`"name": "registry.example/acme/widget@sha256:1`, `"name": "registry.example/acme/widget:1`},
		"path reference":  {`"url": "sbom-widget-`, `"url": "../sbom-widget-`},
		"bad hash":        {`"alg": "SHA-256"`, `"alg": "SHA-1"`},
		"unpinned index":  {`"bom-ref": "` + ref + `"`, `"bom-ref": "registry.example/acme/widget:latest"`},
		"second referrer": {`"type": "bom",`, `"type": "bom", "comment": "x"}, {"type": "bom",`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			changed := strings.Replace(string(body), change[0], change[1], 1)
			require.NotEqual(t, string(body), changed)

			_, _, ok, err := sbom.IndexBOMInventories([]byte(changed))
			require.Error(t, err)
			require.False(t, ok)
		})
	}
}
