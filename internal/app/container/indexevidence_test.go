// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	domainsbom "github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

func stagedIndexEvidenceFixture(t *testing.T, archs []string) (appcontainer.SignerImageAssembleInput, *atomic.Int32) {
	t.Helper()
	t.Chdir(t.TempDir())

	writes := new(atomic.Int32)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writes.Add(1)
		}

		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	require.NoError(t, os.WriteFile("auth.json", []byte(`{"auths":{}}`), 0o600))

	repo := strings.TrimPrefix(server.URL, "http://") + "/org/image"
	for _, arch := range archs {
		_, image := stagedLayout(t, t.Context(), "image-arch-"+arch+"/layout", repo+":staging-v1.2.3-"+arch, arch)
		digest, err := image.Digest()
		require.NoError(t, err)
		body, err := json.Marshal(appcontainer.SignerImageArchMetadata{Arch: arch, Tag: repo + ":staging-v1.2.3-" + arch, Digest: digest.String(), Ref: repo + "@" + digest.String()})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile("image-arch-"+arch+"/image-"+arch+".json", body, 0o600))
	}

	return appcontainer.SignerImageAssembleInput{
		AuthFile: "auth.json", SourceSHA: strings.Repeat("b", 40), ServerURL: server.URL, Repository: "org/image",
		TagName: "staging-v1.2.3", RetryAttempts: 1, Archs: archs, ManifestOutput: "dist/index.json",
		Evidence: &appcontainer.StagedLayoutEvidence{
			Trivy: &fakeImageEvidenceTrivy{}, Syft: &fakeImageEvidenceSyft{}, Stderr: io.Discard,
			TrivyOutputTemplate: "dist/trivy-{arch}.json", SBOMOutputTemplate: "dist/sbom-{arch}.json",
			IndexSBOMOutput: "dist/image-sbom.cyclonedx.json", SBOMURLPrefix: "https://forge.example/org/image/releases/download/v1.2.3",
		},
	}, writes
}

func TestStagedIndexSBOMBindsEveryScannedPlatformWithoutRepulling(t *testing.T) {
	for _, archs := range [][]string{{"amd64"}, {"amd64", "arm64"}} {
		t.Run(strings.Join(archs, "+"), func(t *testing.T) {
			in, _ := stagedIndexEvidenceFixture(t, archs)
			sink := fakeoutputsink.New(t)
			client := ociregistry.WithAuthFile(in.AuthFile)
			meta, err := appcontainer.PublishAndAssembleSignerImage(t.Context(), client, sink, nil, io.Discard, in)
			require.NoError(t, err)
			index, err := os.ReadFile(in.ManifestOutput)
			require.NoError(t, err)

			parts := make([]domainsbom.IndexBOMPart, 0, len(archs))

			for _, arch := range archs {
				document, err := os.ReadFile("dist/sbom-" + arch + ".json")
				require.NoError(t, err)
				layout, err := client.InspectLayout(t.Context(), "image-arch-"+arch+"/layout")
				require.NoError(t, err)

				repo, _, _ := strings.Cut(meta.Ref, "@")
				parts = append(parts, domainsbom.IndexBOMPart{Platform: "linux/" + arch, ImageRef: repo + "@" + layout.Digest,
					URL: in.Evidence.SBOMURLPrefix + "/sbom-" + arch + ".json", SHA256: fmt.Sprintf("%x", sha256.Sum256(document)), Document: document})
				require.FileExists(t, "dist/trivy-"+arch+".json")
			}

			want, err := domainsbom.ComposeIndexBOM(meta.Ref, index, parts)
			require.NoError(t, err)
			got, err := os.ReadFile(in.Evidence.IndexSBOMOutput)
			require.NoError(t, err)
			require.Equal(t, want, got)
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(got)), sink.Single("image-sbom-sha256"))
			// Syft saw only the already-staged layouts, once per platform.
			syft := in.Evidence.Syft.(*fakeImageEvidenceSyft)
			require.Len(t, syft.targets, len(archs))

			for i, arch := range archs {
				require.Equal(t, "oci-dir:image-arch-"+arch+"/layout", syft.targets[i])
			}
		})
	}
}

func TestStagedIndexEvidenceRefusesBadDestinationsBeforeScanningOrUpload(t *testing.T) {
	for _, defect := range []string{"missing inventories", "missing index", "missing URL", "URL only", "HTTP URL", "credential URL", "query URL", "colliding index", "colliding inventories", "inventory URL collision", "auth overwrite", "metadata overwrite", "layout overwrite", "symlink collision"} {
		t.Run(defect, func(t *testing.T) {
			in, writes := stagedIndexEvidenceFixture(t, []string{"amd64", "arm64"})

			switch defect {
			case "missing inventories":
				in.Evidence.SBOMOutputTemplate = ""
			case "missing index":
				in.ManifestOutput = ""
			case "missing URL":
				in.Evidence.SBOMURLPrefix = ""
			case "URL only":
				in.Evidence.IndexSBOMOutput = ""
			case "HTTP URL":
				in.Evidence.SBOMURLPrefix = "http://forge.example/evidence"
			case "credential URL":
				in.Evidence.SBOMURLPrefix = "https://user:secret@forge.example/evidence"
			case "query URL":
				in.Evidence.SBOMURLPrefix = "https://forge.example/evidence?token=secret"
			case "colliding index":
				in.Evidence.IndexSBOMOutput = in.ManifestOutput
			case "colliding inventories":
				in.Evidence.SBOMOutputTemplate = in.Evidence.TrivyOutputTemplate
			case "inventory URL collision":
				in.Evidence.SBOMOutputTemplate = "dist/{arch}/inventory.json"
				in.Evidence.Syft.(*fakeImageEvidenceSyft).jsonBodies = map[string]string{
					"oci-dir:image-arch-amd64/layout": `{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,"metadata":{"component":{"name":"amd64"}}}`,
					"oci-dir:image-arch-arm64/layout": `{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,"metadata":{"component":{"name":"arm64"}}}`,
				}
			case "auth overwrite":
				in.Evidence.IndexSBOMOutput = in.AuthFile
			case "metadata overwrite":
				in.Evidence.IndexSBOMOutput = "image-arch-amd64/image-amd64.json"
			case "layout overwrite":
				in.Evidence.IndexSBOMOutput = "image-arch-amd64/layout/index.json"
			case "symlink collision":
				require.NoError(t, os.Mkdir("dist", 0o700))
				require.NoError(t, os.Symlink("dist", "alias"))

				in.Evidence.IndexSBOMOutput = "alias/index.json"
			}

			sink := fakeoutputsink.New(t)
			_, err := appcontainer.PublishAndAssembleSignerImage(t.Context(), ociregistry.WithAuthFile(in.AuthFile), sink, nil, io.Discard, in)
			require.Error(t, err)
			require.Zero(t, writes.Load())
			require.Empty(t, sink.Keys())
			require.Empty(t, in.Evidence.Syft.(*fakeImageEvidenceSyft).targets)
		})
	}
}
