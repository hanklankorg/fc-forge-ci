// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"io"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

type runtimePromotionRegistry interface {
	SignerManifestRegistry
	imageledger.Registry
}

// RuntimePublicationPromoteInput binds the verified index to its source commit
// and trusted public key. Promotion creates only the same-repository sha tag.
type RuntimePublicationPromoteInput struct {
	Manifest  SignerImageAssembleInput
	PublicKey string
}

// PromoteRuntimePublication verifies the native index and every signature and
// attestation before checking current main immediately before the tag write.
// The sha tag remains the existing convenience pointer; the returned digest ref
// is the immutable consumer pin. A copy failure is never blindly retried here.
func PromoteRuntimePublication(ctx context.Context, registry runtimePromotionRegistry, verifier imageEvidenceVerifier, source provider.BranchHeadResolver, out io.Writer, in RuntimePublicationPromoteInput) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if registry == nil || verifier == nil || source == nil || in.PublicKey == "" {
		return "", fmt.Errorf("runtime promotion requires registry, verifier, source reader and trusted key: %w", errs.ErrUsage)
	}

	meta, final, err := verifyRuntimePromotionInput(ctx, registry, verifier, out, in)
	if err != nil {
		return "", err
	}

	head, err := source.BranchHead(ctx, in.Manifest.ServerURL, in.Manifest.Repository, "main")
	if err != nil {
		return "", err
	}

	if head != in.Manifest.SourceSHA {
		return "", fmt.Errorf("refusing stale runtime publication: main no longer matches the source commit: %w", errs.ErrValidation)
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	if err := promoteRuntimeTag(ctx, registry, meta, final); err != nil {
		return "", err
	}

	if err := verifyRuntimePromotedBytes(ctx, registry, final, meta.Digest); err != nil {
		return "", err
	}

	return meta.Ref, nil
}

func verifyRuntimePromotionInput(ctx context.Context, registry SignerManifestRegistry, verifier imageEvidenceVerifier, out io.Writer, in RuntimePublicationPromoteInput) (SignerImageMetadata, string, error) {
	in.Manifest, _ = runtimeSourceManifest(in.Manifest)

	derived, err := deriveSignerImageManifest(in.Manifest)
	if err != nil {
		return SignerImageMetadata{}, "", err
	}

	meta, err := runtimeIndexMetadata(in.Manifest.LayoutsDir, derived)
	if err != nil {
		return meta, "", err
	}

	if err := VerifyMultiarchImageManifest(ctx, registry, nil, meta.Ref, in.Manifest); err != nil {
		return meta, "", err
	}

	if err := verifyRuntimeImageEvidence(ctx, verifier, out, in, derived, meta.Ref); err != nil {
		return meta, "", err
	}

	return meta, derived.ImageRepository + ":sha-" + in.Manifest.SourceSHA, nil
}

func verifyRuntimePromotedBytes(ctx context.Context, registry RawManifestRegistry, final, expected string) error {
	body, err := registry.Manifest(ctx, final)
	if err != nil {
		return err
	}

	digest, err := digestRawManifest(body, final)
	if err != nil {
		return err
	}

	if digest != expected {
		return fmt.Errorf("promoted runtime index bytes do not match the signed digest: %w", errs.ErrValidation)
	}

	return nil
}

func verifyRuntimeImageEvidence(ctx context.Context, verifier imageEvidenceVerifier, out io.Writer, in RuntimePublicationPromoteInput, derived signerImageManifestDerived, ref string) error {
	if err := verifier.VerifyImage(ctx, domaincontainer.ImageVerifyRequest{ImageRef: ref, KeyRef: in.PublicKey}, out); err != nil {
		return err
	}

	if err := verifier.VerifyAttestation(ctx, domaincontainer.AttestationVerifyRequest{ImageRef: ref, KeyRef: in.PublicKey, PredicateType: domaincontainer.PredicateTypeSLSAProvenance1}, out); err != nil {
		return err
	}

	for _, arch := range derived.Archs {
		child, err := signerArchRef(in.Manifest.LayoutsDir, arch, derived.ImageRepository, derived.Name, derived.ManifestTag+"-"+arch)
		if err != nil {
			return err
		}

		if err := verifier.VerifyImage(ctx, domaincontainer.ImageVerifyRequest{ImageRef: child, KeyRef: in.PublicKey}, out); err != nil {
			return err
		}

		if err := verifier.VerifyAttestation(ctx, domaincontainer.AttestationVerifyRequest{ImageRef: child, KeyRef: in.PublicKey, PredicateType: domaincontainer.PredicateTypeCycloneDX}, out); err != nil {
			return err
		}
	}

	return nil
}

func promoteRuntimeTag(ctx context.Context, registry imageledger.Registry, meta SignerImageMetadata, final string) error {
	if err := registry.CopyTag(ctx, meta.Ref, final); err != nil {
		return err
	}

	digest, err := registry.ResolveDigest(ctx, final)
	if err != nil {
		return err
	}

	if digest != meta.Digest {
		return fmt.Errorf("promoted runtime tag does not serve the signed index digest: %w", errs.ErrValidation)
	}

	return nil
}
