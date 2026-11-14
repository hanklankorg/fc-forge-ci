// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// evidenceRegistry serves an in-memory registry on loopback, with or without
// the Referrers API, and returns its host.
func evidenceRegistry(t *testing.T, referrersAPI bool) string {
	t.Helper()

	server := httptest.NewServer(registry.New(
		registry.Logger(log.New(io.Discard, "", 0)),
		registry.WithReferrersSupport(referrersAPI),
	))
	t.Cleanup(server.Close)

	return strings.TrimPrefix(server.URL, "http://")
}

// A promotion to another registry must deliver what a verifier looks for: the
// image under its digest, every referrer of it, and the cosign tags the source
// holds. Each registry is tried with and without the Referrers API, since the
// two ends of a promotion need not agree.
func TestCopyWithSignatures_CarriesTheEvidence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name               string
		sourceAPI, destAPI bool
	}{
		{"referrers API on both", true, true},
		{"referrers tag on both", false, false},
		{"API to tag", true, false},
		{"tag to API", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			adapter := New()
			source := evidenceRegistry(t, tc.sourceAPI) + "/team/app"
			dest := evidenceRegistry(t, tc.destAPI) + "/sovereign/team/app"
			options := adapter.craneOpts(t.Context(), source, dest)
			remoteOptions := crane.GetOptions(options...).Remote

			image, err := random.Image(256, 1)
			require.NoError(t, err)
			require.NoError(t, crane.Push(image, source+":staging-v1.2.3", options...))

			digest, err := image.Digest()
			require.NoError(t, err)

			size, err := image.Size()
			require.NoError(t, err)

			mediaType, err := image.MediaType()
			require.NoError(t, err)

			sourceRepo, err := name.NewRepository(source, name.Insecure)
			require.NoError(t, err)

			destRepo, err := name.NewRepository(dest, name.Insecure)
			require.NoError(t, err)

			// A signature bundle as a signer attaches it: a manifest whose
			// subject is the image.
			payload, err := random.Image(64, 1)
			require.NoError(t, err)

			bundle, ok := mutate.Subject(payload, v1.Descriptor{MediaType: mediaType, Digest: digest, Size: size}).(v1.Image)
			require.True(t, ok)

			bundleDigest, err := bundle.Digest()
			require.NoError(t, err)
			require.NoError(t, remote.Write(sourceRepo.Digest(bundleDigest.String()), bundle, remoteOptions...))

			// And one under cosign's older tag.
			legacy, err := random.Image(64, 1)
			require.NoError(t, err)

			legacyDigest, err := legacy.Digest()
			require.NoError(t, err)

			signatureTag := strings.ReplaceAll(digest.String(), ":", "-") + ".sig"
			require.NoError(t, crane.Push(legacy, source+":"+signatureTag, options...))

			require.NoError(t, adapter.CopyWithSignatures(t.Context(), source+":staging-v1.2.3", dest+":release"))

			served, err := crane.Digest(dest+":release", options...)
			require.NoError(t, err)
			require.Equal(t, digest.String(), served)

			referrers, err := remote.Referrers(destRepo.Digest(digest.String()), remoteOptions...)
			require.NoError(t, err)

			listed, err := referrers.IndexManifest()
			require.NoError(t, err)
			require.Len(t, listed.Manifests, 1)
			require.Equal(t, bundleDigest, listed.Manifests[0].Digest, "the bundle must be found from the image at the destination")

			servedSignature, err := crane.Digest(dest+":"+signatureTag, options...)
			require.NoError(t, err)
			require.Equal(t, legacyDigest.String(), servedSignature)

			// The source has no attestation tag, and none is invented.
			_, err = crane.Digest(dest+":"+strings.TrimSuffix(signatureTag, ".sig")+".att", options...)
			require.Error(t, err)
		})
	}
}

// An image without evidence is copied as it is; a source that is not there is
// reported as missing and writes nothing.
func TestCopyWithSignatures_WithoutEvidenceAndWithoutSource(t *testing.T) {
	t.Parallel()

	adapter := New()
	source := evidenceRegistry(t, false) + "/team/app"
	dest := evidenceRegistry(t, false) + "/team/app"
	options := adapter.craneOpts(t.Context(), source, dest)

	image, err := random.Image(256, 1)
	require.NoError(t, err)
	require.NoError(t, crane.Push(image, source+":v1", options...))

	digest, err := image.Digest()
	require.NoError(t, err)

	require.NoError(t, adapter.CopyWithSignatures(t.Context(), source+":v1", dest+":release"))

	served, err := crane.Digest(dest+":release", options...)
	require.NoError(t, err)
	require.Equal(t, digest.String(), served)

	err = adapter.CopyWithSignatures(t.Context(), source+":absent", dest+":other")
	require.ErrorIs(t, err, errs.ErrMissingInput)

	_, err = crane.Digest(dest+":other", options...)
	require.Error(t, err)
}
