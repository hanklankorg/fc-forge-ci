// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appartifact "github.com/diggsweden/forge-ci/internal/app/artifact"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/stretchr/testify/require"
)

// An inert implementation of the existing transport ports. No provider,
// credential, registry, signer executable or container is used in this journey.
type pinnedHandoffTransport struct{ files map[string][]byte }

func (s *pinnedHandoffTransport) UploadRunArtifact(_ context.Context, in provider.RunArtifactUpload) (provider.RunArtifactInfo, error) {
	s.files = map[string][]byte{}
	err := fs.WalkDir(os.DirFS(in.Dir), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		body, err := os.ReadFile(filepath.Join(in.Dir, path))
		s.files[path] = body

		return err
	})

	return provider.RunArtifactInfo{Name: in.Name, FileCount: len(s.files)}, err
}

func (s *pinnedHandoffTransport) DownloadRunArtifact(_ context.Context, in provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
	for path, body := range s.files {
		full := filepath.Join(in.Dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return provider.RunArtifactInfo{}, err
		}

		if err := os.WriteFile(full, body, 0o600); err != nil {
			return provider.RunArtifactInfo{}, err
		}
	}

	return provider.RunArtifactInfo{Name: in.Name, FileCount: len(s.files)}, nil
}

func TestCanonicalPinnedHandoffProducerTransportSignerRoundTrip(t *testing.T) {
	// Keep the signer's ordinary workspace different from the verified root.
	t.Chdir(t.TempDir())
	producer := filepath.Join(t.TempDir(), "payload")
	base := ledgerSignEntry(t)
	base.ImageKind, base.Role = imageledger.ImageKindBase, "base"
	base.Ref = "forge.example/examplescope/myapp-base@" + base.Digest
	base.FinalTag = "forge.example/examplescope/myapp-base:" + base.BaseInputID + "-" + base.Flavor
	base.MovingTag = "forge.example/examplescope/myapp-base:" + base.Flavor
	base.CandidateTag = "forge.example/examplescope/myapp-base:staging-" + base.BaseInputID + "-" + base.Flavor
	base.BaseRef = ""
	base.SBOM = imageledger.DefaultSBOMPath(base.ImageKind, base.Flavor, base.Role)
	final := ledgerSignEntry(t)
	entries := []imageledger.Entry{base, final}
	documents := [][]byte{[]byte("{\n \"bomFormat\":\"CycloneDX\",\"serialNumber\":\"producer-base\"\n}\n"), []byte("{\"bomFormat\":\"CycloneDX\",\"serialNumber\":\"producer-final\"}\n")}
	ledgers := []string{"dist/base-images.json", "dist/release-images.json"}

	for idx := range entries {
		entry := &entries[idx]
		sum := sha256.Sum256(documents[idx])
		entry.SBOMSHA256 = hex.EncodeToString(sum[:])
		body, err := json.Marshal([]imageledger.Entry{*entry})
		require.NoError(t, err)

		for path, bytes := range map[string][]byte{entry.SBOM: documents[idx], ledgers[idx]: body} {
			full := filepath.Join(producer, path)
			require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
			require.NoError(t, os.WriteFile(full, bytes, 0o600))
		}
	}

	digest, err := apprelease.CanonicalPayloadDigest(producer)
	require.NoError(t, err)
	// Transport the payload and sibling digest together, through existing ports.
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(producer), "digest"), []byte(digest+"\n"), 0o600))

	transport := &pinnedHandoffTransport{}
	_, err = appartifact.Upload(t.Context(), transport, nil, io.Discard, provider.RunArtifactUpload{Name: "handoff", Dir: filepath.Dir(producer)})
	require.NoError(t, err)
	download := t.TempDir()
	_, err = appartifact.Download(t.Context(), transport, nil, io.Discard, provider.RunArtifactDownload{Name: "handoff", Dir: download})
	require.NoError(t, err)
	expected, err := apprelease.ReadDistDigestFile(filepath.Join(download, "digest"))
	require.NoError(t, err)
	verified := filepath.Join(t.TempDir(), "verified-workspace")
	require.NoError(t, apprelease.StageVerifiedCanonicalPayload(t.Context(), filepath.Join(download, "payload"), verified, expected))
	// Changing the downloaded/producer bytes after staging cannot replace what
	// the signer reads. No copying into or trusting its current workspace.
	require.NoError(t, os.WriteFile(filepath.Join(download, "payload", base.SBOM), []byte("untrusted later replacement"), 0o600))

	for idx, original := range entries {
		body, err := os.ReadFile(filepath.Join(verified, ledgers[idx]))
		require.NoError(t, err)

		var admitted []imageledger.Entry
		if idx == 0 {
			admitted, err = appbaseimages.AdmitBaseImageLedger(body, "forge.example/examplescope/myapp-base")
		} else {
			admitted, err = imageledger.Parse(body)
		}

		require.NoError(t, err)
		require.Equal(t, original, admitted[0])
		in := signInput(t, admitted[0])
		in.EvidenceRoot = verified

		in.Entries = admitted
		if idx == 0 {
			in.ExpectedImageRepository = "forge.example/examplescope/myapp-base"
			in.SBOMPathPattern = `^premade-sboms/base-sbom-[-A-Za-z0-9_.]+[.]cyclonedx[.]json$`
		}

		signer, syft := &recordingImageSigner{}, &fakeLedgerSyft{}
		require.NoError(t, appcontainer.SignLedgerImages(t.Context(), signer, syft, fakeLedgerResolver{original.CandidateTag: original.Digest}, io.Discard, io.Discard, in))
		require.Empty(t, syft.targets)
		require.Len(t, signer.signs, 1)
		require.Len(t, signer.attests, 2)
		require.Equal(t, documents[idx], signer.attests[0].predicate)
		require.Equal(t, filepath.Join(verified, original.SBOM), signer.attests[0].input.PredicatePath)
		require.Contains(t, string(signer.attests[1].predicate), original.SBOM, "provenance retains canonical ledger path")
		require.NotContains(t, string(signer.attests[1].predicate), verified)

		for _, failure := range []string{"pin", "unpinned", "traversal", "absolute", "other root", "alias", "source scope", "symlink"} {
			t.Run(original.Role+"/"+failure, func(t *testing.T) {
				bad := in
				bad.Entries = append([]imageledger.Entry(nil), in.Entries...)

				switch failure {
				case "pin":
					bad.Entries[0].SBOMSHA256 = strings.Repeat("0", 64)
				case "unpinned":
					bad.Entries[0].SBOMSHA256 = ""
				case "traversal":
					bad.Entries[0].SBOM = "../" + original.SBOM
				case "absolute":
					bad.Entries[0].SBOM = filepath.Join(verified, original.SBOM)
				case "other root":
					bad.Entries[0].SBOM = "other/sbom.json"
				case "alias":
					bad.Entries[0].SBOM = "dist/../" + original.SBOM
				case "source scope":
					bad.ExpectedImageRepository = "forge.example/foreign/image"
				case "symlink":
					link := filepath.Join(t.TempDir(), "verified-link")
					require.NoError(t, os.Symlink(verified, link))
					bad.EvidenceRoot = link
				}

				signer, syft := &recordingImageSigner{}, &fakeLedgerSyft{}
				require.Error(t, appcontainer.SignLedgerImages(t.Context(), signer, syft, fakeLedgerResolver{original.CandidateTag: original.Digest}, io.Discard, io.Discard, bad))
				require.Empty(t, signer.signs)
				require.Empty(t, signer.attests)
				require.Empty(t, syft.targets)
			})
		}
	}

	require.NoError(t, apprelease.VerifyCanonicalPayload(verified, expected), "signer must not rewrite verified producer evidence")
}
