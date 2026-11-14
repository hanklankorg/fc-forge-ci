// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	slsaPredicate      = "https://slsa.dev/provenance/v1"
	cycloneDXPredicate = "https://cyclonedx.org/bom"
	bundleMediaType    = "application/vnd.dev.sigstore.bundle.v0.3+json"
)

// evidenceImage is an evidence manifest with one layer of the given type.
func evidenceImage(t *testing.T, mediaType, body string) v1.Image {
	t.Helper()

	image, err := mutate.AppendLayers(empty.Image, static.NewLayer([]byte(body), types.MediaType(mediaType)))
	require.NoError(t, err)

	return image
}

// statementPayload is the DSSE payload of a statement of the predicate type.
func statementPayload(predicateType string) string {
	return base64.StdEncoding.EncodeToString([]byte(`{"_type":"https://in-toto.io/Statement/v1","predicateType":"` + predicateType + `"}`))
}

// bundleOf is a Sigstore bundle holding an attestation of the predicate type.
// Its signature is no signature: the read answers what is stored, verified
// or not.
func bundleOf(predicateType string) string {
	return `{"mediaType":"` + bundleMediaType + `","dsseEnvelope":{"payload":"` + statementPayload(predicateType) +
		`","payloadType":"application/vnd.in-toto+json","signatures":[{"sig":"bm90IGEgc2lnbmF0dXJl"}]}}`
}

// attestedImage pushes an image with an SBOM attestation as a referrer, as a
// signed and SBOM-attested release image has, and returns its repository, its
// digest reference and a function that attaches one more referrer.
func attestedImage(t *testing.T, adapter *Adapter, referrersAPI bool) (string, string, func(mediaType, body string)) {
	t.Helper()

	repository := evidenceRegistry(t, referrersAPI) + "/team/app"
	options := adapter.craneOpts(t.Context(), repository)

	image, err := random.Image(256, 1)
	require.NoError(t, err)
	require.NoError(t, crane.Push(image, repository+":v1", options...))

	digest, err := image.Digest()
	require.NoError(t, err)

	size, err := image.Size()
	require.NoError(t, err)

	mediaType, err := image.MediaType()
	require.NoError(t, err)

	repo, err := name.NewRepository(repository, name.Insecure)
	require.NoError(t, err)

	attach := func(layerType, body string) {
		t.Helper()

		referrer, ok := mutate.Subject(evidenceImage(t, layerType, body), v1.Descriptor{MediaType: mediaType, Digest: digest, Size: size}).(v1.Image)
		require.True(t, ok)

		referrerDigest, err := referrer.Digest()
		require.NoError(t, err)
		require.NoError(t, remote.Write(repo.Digest(referrerDigest.String()), referrer, crane.GetOptions(options...).Remote...))
	}

	attach(bundleMediaType, bundleOf(cycloneDXPredicate))

	return repository, repository + "@" + digest.String(), attach
}

// The registry's answer is about the predicate type asked for, in each place
// cosign stores an attestation: an image with an SBOM attestation alone has
// no provenance, and provenance that carries no valid signature is there.
func TestHasAttestation_ReadsEachStorage(t *testing.T) {
	t.Parallel()

	for _, referrersAPI := range []bool{true, false} {
		adapter := New()
		_, ref, attach := attestedImage(t, adapter, referrersAPI)

		found, err := adapter.HasAttestation(t.Context(), ref, slsaPredicate)
		require.NoError(t, err)
		require.False(t, found, "referrers API %t: an SBOM attestation is no provenance", referrersAPI)

		found, err = adapter.HasAttestation(t.Context(), ref, cycloneDXPredicate)
		require.NoError(t, err)
		require.True(t, found, "referrers API %t", referrersAPI)

		attach(bundleMediaType, bundleOf(slsaPredicate))

		found, err = adapter.HasAttestation(t.Context(), ref, slsaPredicate)
		require.NoError(t, err)
		require.True(t, found, "referrers API %t: provenance stored as a referrer", referrersAPI)

		// cosign's older storage: one DSSE envelope per layer under the
		// sha256-<hex>.att tag. cosign reads every layer there whatever its
		// media type, so a layer typed as something else is read too.
		repository, ref, _ := attestedImage(t, adapter, referrersAPI)
		pushLegacyAttestation(t, adapter, repository, ref, "application/octet-stream")

		found, err = adapter.HasAttestation(t.Context(), ref, slsaPredicate)
		require.NoError(t, err)
		require.True(t, found, "referrers API %t: provenance stored under the .att tag", referrersAPI)
	}
}

// pushLegacyAttestation stores a provenance envelope under the .att tag of
// ref, in a layer of the media type, and returns that layer's digest.
func pushLegacyAttestation(t *testing.T, adapter *Adapter, repository, ref, mediaType string) string {
	t.Helper()

	_, digest, _ := strings.Cut(ref, "@")
	legacy := evidenceImage(t, mediaType, `{"payloadType":"application/vnd.in-toto+json","payload":"`+statementPayload(slsaPredicate)+`","signatures":[]}`)
	require.NoError(t, crane.Push(legacy, repository+":"+strings.ReplaceAll(digest, ":", "-")+".att", adapter.craneOpts(t.Context(), repository)...))

	layers, err := legacy.Layers()
	require.NoError(t, err)

	layerDigest, err := layers[0].Digest()
	require.NoError(t, err)

	return layerDigest.String()
}

// Neither a registry that was not reached nor stored evidence that cannot be
// read is the answer "no attestation". An .att tag whose layer the registry
// no longer serves is such evidence: only a missing tag is no attestation.
func TestHasAttestation_AnUnansweredReadIsNoAbsence(t *testing.T) {
	t.Parallel()

	adapter := New()

	_, err := adapter.HasAttestation(t.Context(), "127.0.0.1:1/team/app@sha256:"+strings.Repeat("a", 64), slsaPredicate)
	require.ErrorIs(t, err, errs.ErrDependencyUnavailable)

	_, ref, attach := attestedImage(t, adapter, false)
	attach(bundleMediaType, `{"dsseEnvelope":{"payload":"not a statement"}}`)

	_, err = adapter.HasAttestation(t.Context(), ref, slsaPredicate)
	require.ErrorIs(t, err, errs.ErrValidation)

	repository, ref, _ := attestedImage(t, adapter, false)
	layer := pushLegacyAttestation(t, adapter, repository, ref, dsseEnvelopeMediaType)
	host, path, _ := strings.Cut(repository, "/")

	request, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, "http://"+host+"/v2/"+path+"/blobs/"+layer, nil)
	require.NoError(t, err)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusAccepted, response.StatusCode)

	found, err := adapter.HasAttestation(t.Context(), ref, slsaPredicate)
	require.ErrorIs(t, err, errs.ErrMissingInput)
	require.False(t, found)

	_, err = adapter.HasAttestation(t.Context(), "127.0.0.1:1/team/app:v1", slsaPredicate)
	require.ErrorIs(t, err, errs.ErrUsage)
}
