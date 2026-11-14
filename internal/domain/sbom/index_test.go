// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/sbom"
)

func indexBOMFixture(t *testing.T) (string, []byte, []sbom.IndexBOMPart) {
	t.Helper()

	repository := "registry.example/acme/widget"
	parts := make([]sbom.IndexBOMPart, 0, 2)
	children := make([]any, 0, 2)

	for index, arch := range []string{"arm64", "amd64"} {
		digest := "sha256:" + strings.Repeat(strconv.Itoa(index+1), 64)
		body := []byte(fmt.Sprintf(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"native-%s","bom-ref":"shared-reference"}]}`, arch))
		parts = append(parts, sbom.IndexBOMPart{Platform: "linux/" + arch, ImageRef: repository + "@" + digest, URL: "https://downloads.example/widget/" + arch + ".json", SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Document: body})
		children = append(children, map[string]any{"digest": digest, "platform": map[string]string{"os": "linux", "architecture": arch}})
	}

	raw, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": children})
	require.NoError(t, err)

	return fmt.Sprintf("%s@sha256:%x", repository, sha256.Sum256(raw)), raw, parts
}

func TestIndexBOMDeterministicallyLinksUnchangedDocuments(t *testing.T) {
	t.Parallel()
	ref, raw, parts := indexBOMFixture(t)
	body, err := sbom.ComposeIndexBOM(ref, raw, parts)
	require.NoError(t, err)

	reversed := slices.Clone(parts)
	slices.Reverse(reversed)
	again, err := sbom.ComposeIndexBOM(ref, raw, reversed)
	require.NoError(t, err)
	require.Equal(t, body, again)
	require.Equal(t, "linux/arm64", parts[0].Platform, "caller input was reordered")

	var document struct {
		Components []struct {
			Name     string `json:"name"`
			External []struct {
				URL    string `json:"url"`
				Hashes []struct {
					Content string `json:"content"`
				} `json:"hashes"`
			} `json:"externalReferences"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(body, &document))
	require.Len(t, document.Components, 2)

	for index, part := range reversed {
		require.Equal(t, part.ImageRef, document.Components[index].Name)
		require.Equal(t, part.URL, document.Components[index].External[0].URL)
		require.Equal(t, part.SHA256, document.Components[index].External[0].Hashes[0].Content)
	}

	require.NotContains(t, string(body), "shared-reference", "native bom-refs must not be flattened or rewritten")
}

func TestIndexBOMRefusesIncompleteOrChangedEvidence(t *testing.T) {
	t.Parallel()

	for _, defect := range []string{"missing", "duplicate", "wrong-platform", "wrong-image", "foreign-repository", "bad-checksum", "changed-document", "non-bom", "bad-index", "url-credentials", "url-query", "url-http", "url-conflict"} {
		t.Run(defect, func(t *testing.T) {
			t.Parallel()
			ref, raw, parts := indexBOMFixture(t)

			switch defect {
			case "missing":
				parts = parts[:1]
			case "duplicate":
				parts = append(parts, parts[0])
			case "wrong-platform":
				parts[0].Platform = "linux/ppc64le"
			case "wrong-image":
				parts[0].ImageRef = "registry.example/acme/widget@sha256:" + strings.Repeat("f", 64)
			case "foreign-repository":
				parts[0].ImageRef = strings.ReplaceAll(parts[0].ImageRef, "acme/widget", "other/image")
			case "bad-checksum":
				parts[0].SHA256 = "bad"
			case "changed-document":
				parts[0].Document = append(parts[0].Document, ' ')
			case "non-bom":
				parts[0].Document = []byte(`{}`)
				parts[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256(parts[0].Document))
			case "bad-index":
				raw = append(raw, ' ')
			case "url-credentials":
				parts[0].URL = "https://user:private@downloads.example/bom"
			case "url-query":
				parts[0].URL = "https://downloads.example/bom?token=private"
			case "url-http":
				parts[0].URL = "http://downloads.example/bom"
			case "url-conflict":
				parts[0].URL = parts[1].URL
			}

			body, err := sbom.ComposeIndexBOM(ref, raw, parts)
			require.Error(t, err)
			require.Empty(t, body)
			require.NotContains(t, err.Error(), "private")
		})
	}
}
