// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/stretchr/testify/require"
)

func runtimeSigningFixture(t *testing.T) (appcontainer.RuntimePublicationSignInput, *fakeSignerImageTool) {
	t.Helper()

	manifest, registry := checkedIndexFixture(t)
	for _, labels := range registry.labels {
		labels["org.opencontainers.image.source"] = "https://forge.example/acme/widget"
		labels["org.opencontainers.image.revision"] = manifest.SourceSHA
	}

	meta := appcontainer.SignerImageMetadata{
		Tag:    manifest.ImageRepository + ":" + manifest.TagName,
		Digest: manifestTestDigest(registry.raw), Ref: manifest.ImageRepository + "@" + manifestTestDigest(registry.raw),
	}
	body, err := json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("image.json", body, 0o600))

	var checksums strings.Builder

	for _, arch := range manifest.Archs {
		sbom := []byte(`{"bomFormat":"CycloneDX","metadata":{"architecture":"` + arch + `"}}`)
		name := "sbom-" + arch + ".json"
		require.NoError(t, os.WriteFile(name, sbom, 0o600))
		_, err := fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(sbom), name)
		require.NoError(t, err)
	}

	require.NoError(t, os.WriteFile("sboms.sha256", []byte(checksums.String()), 0o600))

	return appcontainer.RuntimePublicationSignInput{
		Manifest: manifest,
		Signing:  appcontainer.SignImageInput{Method: domainrelease.SignMethodKMS, KeyRef: "env://COSIGN_KEY"},
		Provenance: provenance.Input{
			BuildType: provenance.ContainerBuildType, BuilderID: "https://forge.example/acme/widget/.forgejo/workflows/runtime-images.yml@refs/heads/main",
			Ref: "refs/heads/main", Flavor: "go", InvocationID: "https://forge.example/acme/widget/actions/runs/123",
		},
	}, registry
}

func TestRuntimeSigningReusesNativeVerificationAndPrebuiltSBOMs(t *testing.T) {
	in, registry := runtimeSigningFixture(t)
	signer := &recordingImageSigner{beforeSign: func() {
		require.Contains(t, registry.events, "raw", "index verification must finish before signing")
	}}
	require.NoError(t, appcontainer.SignRuntimePublication(t.Context(), signer, registry, io.Discard, in))
	require.Len(t, signer.signs, 1)
	require.True(t, signer.signs[0].Recursive, "children must be signed as well as the index")
	require.Len(t, signer.attests, 3)

	for index, arch := range in.Manifest.Archs {
		attestation := signer.attests[index]
		require.Equal(t, "cyclonedx", attestation.input.PredicateType)
		require.Equal(t, "sbom-"+arch+".json", attestation.input.PredicatePath)
		require.Contains(t, string(attestation.predicate), arch)
	}

	last := signer.attests[2]
	require.Equal(t, "slsaprovenance1", last.input.PredicateType)
	require.Contains(t, string(last.predicate), in.Manifest.SourceSHA)
	require.Contains(t, string(last.predicate), "git+https://forge.example/acme/widget")
	require.Contains(t, string(last.predicate), `"flavor": "go"`)
	require.Equal(t, signer.signs[0].ImageRef, last.input.ImageRef)
	require.NoFileExists(t, last.input.PredicatePath, "generated provenance is private temporary state")
	require.Empty(t, registry.mergeTags, "signing cannot publish a new index")
}

func TestRuntimeSigningRefusesAllBadEvidenceBeforeRegistryOrSigning(t *testing.T) {
	for _, failure := range []string{"second SBOM changed", "duplicate checksums", "missing architecture", "wrong index", "non-object SBOM", "symlink SBOM", "bad native metadata", "wrong ref", "inconsistent signing identity"} {
		t.Run(failure, func(t *testing.T) {
			in, registry := runtimeSigningFixture(t)

			switch failure {
			case "second SBOM changed":
				require.NoError(t, os.WriteFile("sbom-arm64.json", []byte(`{"changed":true}`), 0o600))
			case "duplicate checksums":
				require.NoError(t, os.WriteFile("sboms.sha256", []byte(strings.Repeat("a", 64)+"  sbom-amd64.json\n"+strings.Repeat("a", 64)+"  sbom-amd64.json\n"), 0o600))
			case "missing architecture":
				require.NoError(t, os.WriteFile("sboms.sha256", []byte(strings.Repeat("a", 64)+"  sbom-amd64.json\n"), 0o600))
			case "wrong index":
				require.NoError(t, os.WriteFile("image.json", []byte(`{"tag":"another-run","digest":"sha256:`+strings.Repeat("a", 64)+`","ref":"registry.other/acme/widget@sha256:`+strings.Repeat("a", 64)+`"}`), 0o600))
			case "non-object SBOM":
				require.NoError(t, os.WriteFile("sbom-arm64.json", []byte(`null`), 0o600))

				sum := fmt.Sprintf("%x", sha256.Sum256([]byte(`null`)))
				original, err := os.ReadFile("sboms.sha256")
				require.NoError(t, err)

				first, _, _ := strings.Cut(string(original), "\n")
				require.NoError(t, os.WriteFile("sboms.sha256", []byte(first+"\n"+sum+"  sbom-arm64.json\n"), 0o600))
			case "symlink SBOM":
				require.NoError(t, os.Rename("sbom-arm64.json", "other.json"))
				require.NoError(t, os.Symlink("other.json", "sbom-arm64.json"))
			case "bad native metadata":
				require.NoError(t, os.WriteFile("widget-arch-arm64/widget-arm64.json", []byte(`{}`), 0o600))
			case "wrong ref":
				in.Provenance.Ref = "refs/heads/topic"
			case "inconsistent signing identity":
				in.Signing.OIDCIssuer = "https://unexpected.example"
			}

			signer := &recordingImageSigner{}
			require.Error(t, appcontainer.SignRuntimePublication(t.Context(), signer, registry, io.Discard, in))
			require.Empty(t, signer.signs)
			require.Empty(t, signer.attests)
			require.Empty(t, registry.events, "local evidence refusal must precede registry reads")
		})
	}
}

func TestRuntimeSigningRejectsRegistrySubstitutionBeforeSigning(t *testing.T) {
	in, registry := runtimeSigningFixture(t)
	registry.raw = []byte(`{"changed":"index"}`)
	signer := &recordingImageSigner{}
	require.Error(t, appcontainer.SignRuntimePublication(t.Context(), signer, registry, io.Discard, in))
	require.Empty(t, signer.signs)
	require.Empty(t, signer.attests)
}

func TestRuntimeSigningRequiresEveryNativeImageToMatchTheSource(t *testing.T) {
	for _, label := range []string{"org.opencontainers.image.source", "org.opencontainers.image.revision"} {
		t.Run(label, func(t *testing.T) {
			in, registry := runtimeSigningFixture(t)
			registry.labels[in.Manifest.ImageRepository+"@sha256:"+strings.Repeat("2", 64)][label] = "another-source"
			signer := &recordingImageSigner{}
			require.Error(t, appcontainer.SignRuntimePublication(t.Context(), signer, registry, io.Discard, in))
			require.Empty(t, signer.signs)
			require.Empty(t, signer.attests)
		})
	}
}

func TestRuntimeSigningCancellationPrecedesReads(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	signer := &recordingImageSigner{}
	err := appcontainer.SignRuntimePublication(ctx, signer, nil, io.Discard, appcontainer.RuntimePublicationSignInput{})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, signer.signs)
}

func TestRuntimeSigningStopsAtTheFirstPublicationFailure(t *testing.T) {
	operations := []string{"sign", "attest:cyclonedx", "attest:cyclonedx", "attest:slsaprovenance1"}
	for failAt := 1; failAt <= len(operations); failAt++ {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			in, registry := runtimeSigningFixture(t)
			signer := &failingImageSigner{failAt: failAt}
			err := appcontainer.SignRuntimePublication(t.Context(), signer, registry, io.Discard, in)
			require.ErrorIs(t, err, errPublicationRefused)
			require.Equal(t, operations[:failAt], signer.calls, "uncertain signing mutations are not retried")
		})
	}
}
