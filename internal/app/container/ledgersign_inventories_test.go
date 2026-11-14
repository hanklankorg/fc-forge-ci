// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	domainsbom "github.com/diggsweden/forge-ci/internal/domain/sbom"
)

// indexLedgerEntry writes a pinned index SBOM and its two platform inventories
// into dist and returns the ledger entry of the index they describe.
func indexLedgerEntry(t *testing.T) (imageledger.Entry, map[string][]byte) {
	t.Helper()

	repository := "forge.example/examplescope/myapp"
	inventories := map[string][]byte{}
	parts := make([]domainsbom.IndexBOMPart, 0, 2)
	children := make([]any, 0, 2)

	for index, arch := range []string{"amd64", "arm64"} {
		digest := "sha256:" + strings.Repeat(strconv.Itoa(index+1), 64)
		body := fmt.Appendf(nil, `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"lib-%s"}]}`+"\n", arch)
		name := "sbom-rust-" + arch + ".json"
		inventories[repository+"@"+digest] = body
		parts = append(parts, domainsbom.IndexBOMPart{Platform: "linux/" + arch, ImageRef: repository + "@" + digest, Name: name, SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Document: body})
		children = append(children, map[string]any{"digest": digest, "platform": map[string]string{"os": "linux", "architecture": arch}})

		require.NoError(t, os.MkdirAll("dist", 0o750))
		require.NoError(t, os.WriteFile("dist/"+name, body, 0o600))
	}

	index, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": children})
	require.NoError(t, err)

	entry := ledgerSignEntry(t)
	entry.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(index))
	entry.Ref = repository + "@" + entry.Digest

	bom, err := domainsbom.ComposeIndexBOM(entry.Ref, index, parts)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(entry.SBOM, bom, 0o600))

	entry.SBOMSHA256 = fmt.Sprintf("%x", sha256.Sum256(bom))

	return entry, inventories
}

// The index SBOM names each platform by child digest and its inventory by
// checksum. The signer attests the index SBOM on the index and each inventory
// on its own child digest, in the repository it signs, so one platform's
// inventory is found on that platform's image.
func TestSignLedgerImages_AttestsEachPlatformInventoryOnItsChildDigest(t *testing.T) {
	t.Chdir(t.TempDir())

	entry, inventories := indexLedgerEntry(t)
	signer := &recordingImageSigner{}

	err := appcontainer.SignLedgerImages(context.Background(), signer, &fakeLedgerSyft{}, fakeLedgerResolver{entry.CandidateTag: entry.Digest}, &bytes.Buffer{}, io.Discard, signInput(t, entry))
	require.NoError(t, err)
	require.Len(t, signer.attests, 4, "index SBOM, two platform inventories, provenance")

	indexRef := entry.CandidateTag + "@" + entry.Digest
	require.Equal(t, indexRef, signer.attests[0].input.ImageRef)
	require.Equal(t, string(domaincontainer.PredicateTypeCycloneDX), signer.attests[0].input.PredicateType)

	attested := map[string][]byte{}

	for _, attest := range signer.attests[1:3] {
		require.Equal(t, string(domaincontainer.PredicateTypeCycloneDX), attest.input.PredicateType)

		attested[attest.input.ImageRef] = attest.predicate
	}

	require.Equal(t, inventories, attested, "each inventory is attested on the child digest the index SBOM names for it")
	require.Equal(t, indexRef, signer.attests[3].input.ImageRef)
	require.Equal(t, string(domaincontainer.PredicateTypeSLSAProvenance1), signer.attests[3].input.PredicateType)
}

// An inventory that is not the one the index SBOM records, or an index SBOM
// of another image, is refused before the first signature.
func TestSignLedgerImages_RefusesInventoriesTheIndexSBOMDoesNotBind(t *testing.T) {
	for _, defect := range []string{"changed inventory", "missing inventory", "index SBOM of another image"} {
		t.Run(defect, func(t *testing.T) {
			t.Chdir(t.TempDir())

			entry, _ := indexLedgerEntry(t)

			switch defect {
			case "changed inventory":
				require.NoError(t, os.WriteFile("dist/sbom-rust-arm64.json", []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`), 0o600))
			case "missing inventory":
				require.NoError(t, os.Remove("dist/sbom-rust-amd64.json"))
			case "index SBOM of another image":
				other, _ := indexLedgerEntry(t)
				entry.Digest = "sha256:" + strings.Repeat("9", 64)
				entry.Ref = "forge.example/examplescope/myapp@" + entry.Digest
				entry.SBOMSHA256 = other.SBOMSHA256
			}

			signer := &recordingImageSigner{}

			err := appcontainer.SignLedgerImages(context.Background(), signer, &fakeLedgerSyft{}, fakeLedgerResolver{entry.CandidateTag: entry.Digest}, &bytes.Buffer{}, io.Discard, signInput(t, entry))
			require.Error(t, err)

			if defect != "missing inventory" {
				require.ErrorIs(t, err, errs.ErrValidation)
			}

			assertNothingPublished(t, signer)
		})
	}
}
