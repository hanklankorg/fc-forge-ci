// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// cosignTagSuffixes are the tags cosign's older storage hangs evidence on,
// sha256-<hex> followed by one of these: signatures, attestations and SBOMs.
var cosignTagSuffixes = []string{".sig", ".att", ".sbom"}

// CopyWithSignatures copies the image source names to dest, preserving its
// digest, together with the evidence the source repository holds for that
// digest. It is the copy of a promotion to another repository or registry,
// where the destination shares nothing with the source.
//
// Evidence is found the two ways a signer stores it. Every OCI referrer of
// the digest is copied by its own digest; a signature or attestation bundle
// is one. Where the destination has no Referrers API the library files each
// under the referrers tag, which is how a verifier finds it there. The tags
// of cosign's older storage are copied when the source has them.
//
// The image goes first, so a failure leaves at worst an image without all of
// its evidence, which a verifier refuses, and never evidence without its image.
// An image that has no evidence is copied as it is: whether a promoted image
// must be signed is the verifier's to decide.
func (a *Adapter) CopyWithSignatures(ctx context.Context, source, dest string) error {
	sourceRef, err := a.parse(source)
	if err != nil {
		return fmt.Errorf("copy %s: %w: %w", source, err, errs.ErrUsage)
	}

	destRef, err := a.parse(dest)
	if err != nil {
		return fmt.Errorf("copy to %s: %w: %w", dest, err, errs.ErrUsage)
	}

	options := a.craneOpts(ctx, source, dest)

	// Resolved once: the evidence copied below is that of the image copied
	// here, whatever a tag in source names a moment later.
	digest, err := crane.Digest(source, options...)
	if err != nil {
		return fmt.Errorf("resolve %s: %w: %w", source, err, classifyRegistryError(err))
	}

	from, to := sourceRef.Context(), destRef.Context()
	subject := from.Digest(digest)

	if err := crane.Copy(subject.String(), dest, options...); err != nil {
		return fmt.Errorf("copy %s -> %s: %w: %w", subject, dest, err, classifyRegistryError(err))
	}

	referrers, err := remote.Referrers(subject, crane.GetOptions(options...).Remote...)
	if err != nil {
		return fmt.Errorf("list the evidence of %s: %w: %w", subject, err, classifyRegistryError(err))
	}

	listed, err := referrers.IndexManifest()
	if err != nil {
		return fmt.Errorf("read the evidence list of %s: %w: %w", subject, err, errs.ErrValidation)
	}

	for _, referrer := range listed.Manifests {
		evidence := referrer.Digest.String()
		if err := crane.Copy(from.Digest(evidence).String(), to.Digest(evidence).String(), options...); err != nil {
			return fmt.Errorf("copy evidence %s of %s to %s: %w: %w", evidence, subject, to, err, classifyRegistryError(err))
		}
	}

	return a.copyCosignTags(from, to, digest, options)
}

// copyCosignTags copies the cosign evidence tags of digest that from holds.
// A tag the source does not have is not an error: no evidence of that kind
// was stored that way.
func (a *Adapter) copyCosignTags(from, to name.Repository, digest string, options []crane.Option) error {
	base := strings.ReplaceAll(digest, ":", "-")

	for _, suffix := range cosignTagSuffixes {
		tag := base + suffix

		err := crane.Copy(from.Tag(tag).String(), to.Tag(tag).String(), options...)
		if err != nil && !errors.Is(classifyRegistryError(err), errs.ErrMissingInput) {
			return fmt.Errorf("copy evidence tag %s to %s: %w: %w", tag, to, err, classifyRegistryError(err))
		}
	}

	return nil
}
