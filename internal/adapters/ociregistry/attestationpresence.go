// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

const (
	// sigstoreBundleMediaTypePrefix starts the media type of the layer cosign
	// stores a signature or attestation bundle in, under an OCI referrer.
	sigstoreBundleMediaTypePrefix = "application/vnd.dev.sigstore.bundle"
	// dsseEnvelopeMediaType is the layer of cosign's older storage, one
	// envelope per attestation under the sha256-<hex>.att tag.
	dsseEnvelopeMediaType = "application/vnd.dsse.envelope.v1+json"
	// maxStoredAttestationSize is the largest layer read, cosign's own
	// default for an attachment it downloads (COSIGN_MAX_ATTACHMENT_SIZE).
	maxStoredAttestationSize = 128 << 20
)

// storedAttestation reads the DSSE payload from either storage: the envelope
// itself in the older one, the envelope inside a Sigstore bundle in the newer.
type storedAttestation struct {
	Payload      string `json:"payload"`
	DSSEEnvelope *struct {
		Payload string `json:"payload"`
	} `json:"dsseEnvelope"`
}

// HasAttestation reports whether the registry stores, for the digest ref
// names, an attestation whose in-toto statement has the given predicate type.
// It reads what is stored and verifies nothing: whether an attestation exists
// is a question apart from whether it verifies, and a caller that needs both
// answers must not take the first from a failed verification.
//
// Both of cosign's storages are read: the OCI referrers of the digest, through
// the Referrers API or the referrers tag where the registry has none, and the
// sha256-<hex>.att tag. No referrer and no tag is the answer false. A registry
// that could not be asked, evidence a manifest names and the registry does not
// serve, and stored evidence that cannot be read as an attestation, are errors
// and never that answer.
//
// It guards against an attestation that does not match and one that was
// changed. It cannot guard against whoever controls the registry deleting the
// provenance, which leaves an image that has none.
func (a *Adapter) HasAttestation(ctx context.Context, ref, predicateType string) (bool, error) {
	parsed, err := a.parse(ref)
	if err != nil {
		return false, fmt.Errorf("parse image ref %s: %w: %w", ref, err, errs.ErrUsage)
	}

	subject, ok := parsed.(name.Digest)
	if !ok {
		return false, fmt.Errorf("stored attestations are read by digest, not by tag: %s: %w", ref, errs.ErrUsage)
	}

	options := a.remoteOpts(ctx)

	referrers, err := remote.Referrers(subject, options...)
	if err != nil {
		return false, fmt.Errorf("list the evidence of %s: %w: %w", subject, err, classifyRegistryError(err))
	}

	listed, err := referrers.IndexManifest()
	if err != nil {
		return false, fmt.Errorf("read the evidence list of %s: %w: %w", subject, err, errs.ErrValidation)
	}

	repository := subject.Context()

	for _, referrer := range listed.Manifests {
		ref := repository.Digest(referrer.Digest.String())

		image, err := remote.Image(ref, options...)
		if err != nil {
			return false, fmt.Errorf("fetch evidence %s: %w: %w", ref, err, classifyRegistryError(err))
		}

		// A referrer is any artifact about the image; only a Sigstore bundle
		// layer can hold an attestation.
		found, err := imageHasAttestation(ref, image, predicateType, false)
		if err != nil || found {
			return found, err
		}
	}

	legacy := repository.Tag(strings.ReplaceAll(subject.DigestStr(), ":", "-") + ".att")

	// Only the tag that is not there means no attestation stored this way. A
	// layer the tag's manifest names and the registry does not serve is
	// evidence that could not be read.
	image, err := remote.Image(legacy, options...)
	if err != nil {
		class := classifyRegistryError(err)
		if errors.Is(class, errs.ErrMissingInput) {
			return false, nil
		}

		return false, fmt.Errorf("fetch evidence %s: %w: %w", legacy, err, class)
	}

	// Every layer under the tag is an attestation to cosign, whatever its
	// media type says.
	return imageHasAttestation(legacy, image, predicateType, true)
}

// imageHasAttestation reads the layers of one evidence manifest and reports
// whether one holds an attestation of the predicate type. With everyLayer
// false, a layer that is no DSSE envelope or Sigstore bundle by its media type
// is passed over.
func imageHasAttestation(ref name.Reference, image v1.Image, predicateType string, everyLayer bool) (bool, error) {
	layers, err := image.Layers()
	if err != nil {
		return false, fmt.Errorf("read evidence %s: %w: %w", ref, err, classifyRegistryError(err))
	}

	for _, layer := range layers {
		stored, err := attestationPredicateType(layer, everyLayer)
		if err != nil {
			return false, fmt.Errorf("read evidence %s: %w", ref, err)
		}

		if stored == predicateType {
			return true, nil
		}
	}

	return false, nil
}

// attestationPredicateType returns the predicate type of the attestation a
// layer holds, and "" for a layer that holds none: another artifact, or a
// bundle of a signature over something other than a statement.
func attestationPredicateType(layer v1.Layer, everyLayer bool) (string, error) {
	if !everyLayer {
		mediaType, err := layer.MediaType()
		if err != nil {
			return "", fmt.Errorf("read layer media type: %w: %w", err, classifyRegistryError(err))
		}

		if mediaType != dsseEnvelopeMediaType && !strings.HasPrefix(string(mediaType), sigstoreBundleMediaTypePrefix) {
			return "", nil
		}
	}

	body, err := readStoredAttestation(layer)
	if err != nil {
		return "", err
	}

	var stored storedAttestation
	if err := json.Unmarshal(body, &stored); err != nil {
		return "", fmt.Errorf("parse stored attestation: %w: %w", err, errs.ErrValidation)
	}

	payload := stored.Payload
	if stored.DSSEEnvelope != nil {
		payload = stored.DSSEEnvelope.Payload
	}

	if payload == "" {
		return "", nil
	}

	statement, err := provenance.DecodeStatement(payload)
	if err != nil {
		return "", fmt.Errorf("parse stored attestation: %w: %w", err, errs.ErrValidation)
	}

	return provenance.StatementString(statement, "predicateType"), nil
}

// readStoredAttestation reads one evidence layer, at most
// maxStoredAttestationSize of it.
func readStoredAttestation(layer v1.Layer) ([]byte, error) {
	blob, err := layer.Compressed()
	if err != nil {
		return nil, fmt.Errorf("fetch layer: %w: %w", err, classifyRegistryError(err))
	}
	defer func() { _ = blob.Close() }()

	body, err := io.ReadAll(io.LimitReader(blob, maxStoredAttestationSize+1))
	if err != nil {
		return nil, fmt.Errorf("fetch layer: %w: %w", err, classifyRegistryError(err))
	}

	if len(body) > maxStoredAttestationSize {
		return nil, fmt.Errorf("stored attestation is larger than %d bytes: %w", maxStoredAttestationSize, errs.ErrValidation)
	}

	return body, nil
}
