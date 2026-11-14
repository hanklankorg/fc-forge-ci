// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/diggsweden/forge-ci/internal/cliio"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

// RuntimePublicationSignInput binds retained native evidence to the prepared
// index. Signing recursively covers its children; SBOMs are never regenerated
// in this trusted job. Signing.Image and Signing.Recursive are derived here.
type RuntimePublicationSignInput struct {
	Manifest   SignerImageAssembleInput
	Signing    SignImageInput
	Provenance provenance.Input
}

type runtimeSBOM struct {
	ref, path string
}

// SignRuntimePublication validates all retained SBOMs, provenance and the exact
// multiarch index before the first signing mutation. It reuses multiarch-image
// verification, ledger prebuilt-SBOM validation and generic image sign/attest.
func SignRuntimePublication(ctx context.Context, signer ledgerImageSigner, registry SignerManifestRegistry, out io.Writer, in RuntimePublicationSignInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if signer == nil || registry == nil {
		return fmt.Errorf("runtime signing requires image signer and registry adapters: %w", errs.ErrUsage)
	}

	if in.Provenance.Ref != "refs/heads/main" {
		return fmt.Errorf("runtime signing requires the main publication ref: %w", errs.ErrValidation)
	}

	manifest, source := runtimeSourceManifest(in.Manifest)
	in.Manifest = manifest

	derived, err := deriveSignerImageManifest(in.Manifest)
	if err != nil {
		return err
	}

	meta, err := runtimeIndexMetadata(in.Manifest.LayoutsDir, derived)
	if err != nil {
		return err
	}

	in.Signing.Image, in.Signing.Recursive = meta.Ref, true
	if err := validateRuntimeSigningIdentity(in.Signing); err != nil {
		return err
	}

	sboms, err := runtimeSBOMs(ctx, in.Manifest.LayoutsDir, derived, out)
	if err != nil {
		return err
	}

	in.Provenance.SourceURI = "git+" + source
	in.Provenance.ImageName = meta.Ref
	in.Provenance.ResolvedDeps = []provenance.Dependency{provenance.SourceDependency(source, in.Provenance.Ref, in.Manifest.SourceSHA)}
	attestation := runtimeAttestation(in.Signing, in.Provenance)

	predicatePath, predicateType, cleanup, err := resolvePredicate(attestation)
	if err != nil {
		return err
	}
	defer cleanup()

	attestation.PredicatePath, attestation.PredicateType = predicatePath, predicateType

	if err := VerifyMultiarchImageManifest(ctx, registry, nil, meta.Ref, in.Manifest); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := SignImage(ctx, signer, out, in.Signing); err != nil {
		return err
	}

	for _, sbom := range sboms {
		child := attestation
		child.Image, child.PredicateType, child.PredicatePath = sbom.ref, domaincontainer.PredicateTypeCycloneDX, sbom.path

		if err := AttestImage(ctx, signer, out, child); err != nil {
			return err
		}
	}

	return AttestImage(ctx, signer, out, attestation)
}

func runtimeIndexMetadata(root string, derived signerImageManifestDerived) (SignerImageMetadata, error) {
	var meta SignerImageMetadata

	body, err := cliio.ReadRegularFile(filepath.Join(root, "image.json"))
	if err != nil {
		return meta, err
	}

	if err := decodeRuntimeJSON(body, &meta); err != nil {
		return meta, err
	}

	if meta.Tag != derived.ManifestTag || !domaincontainer.ValidDigest(meta.Digest) || meta.Ref != derived.ImageRepository+"@"+meta.Digest {
		return meta, fmt.Errorf("runtime index metadata must match the expected tag, repository and digest: %w", errs.ErrValidation)
	}

	return meta, nil
}

func runtimeSBOMs(ctx context.Context, root string, derived signerImageManifestDerived, out io.Writer) ([]runtimeSBOM, error) {
	body, err := cliio.ReadRegularFile(filepath.Join(root, "sboms.sha256"))
	if err != nil {
		return nil, err
	}

	subjects, err := provenance.ParseChecksums(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	checksums := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		if _, duplicate := checksums[subject.Name]; duplicate {
			return nil, fmt.Errorf("duplicate runtime SBOM checksum: %w", errs.ErrValidation)
		}

		checksums[subject.Name] = subject.SHA256
	}

	if len(checksums) != len(derived.Archs) {
		return nil, fmt.Errorf("runtime SBOM checksums must cover exactly the selected native architectures: %w", errs.ErrValidation)
	}

	result := make([]runtimeSBOM, 0, len(derived.Archs))
	for _, arch := range derived.Archs {
		name := "sbom-" + arch + ".json"
		checksum := checksums[name]

		if checksum == "" {
			return nil, fmt.Errorf("runtime SBOM checksum missing for %s: %w", arch, errs.ErrValidation)
		}

		path := filepath.Join(root, name)
		if err := ensureImageSBOM(ctx, nil, out, "", imageledger.Entry{SBOM: path, SBOMSHA256: checksum}); err != nil {
			return nil, err
		}

		ref, err := signerArchRef(root, arch, derived.ImageRepository, derived.Name, derived.ManifestTag+"-"+arch)
		if err != nil {
			return nil, err
		}

		result = append(result, runtimeSBOM{ref: ref, path: path})
	}

	return result, nil
}

func validateRuntimeSigningIdentity(in SignImageInput) error {
	if in.Method != domainrelease.SignMethodKMS && in.Method != domainrelease.SignMethodSigstore {
		return fmt.Errorf("runtime image signing requires kms or sigstore: %w", errs.ErrInvalidConfig)
	}

	return (domaincontainer.ImageSignRequest{
		ImageRef: in.Image, Recursive: true, Keyless: in.Method == domainrelease.SignMethodSigstore,
		KeyRef: in.KeyRef, OIDCIssuer: in.OIDCIssuer, FulcioURL: in.FulcioURL,
		RekorURL: in.RekorURL, TrustedRootPath: in.TrustedRootPath,
	}).Validate()
}

func runtimeAttestation(signing SignImageInput, prov provenance.Input) AttestImageInput {
	return AttestImageInput{
		Image: signing.Image, Method: signing.Method, PredicateType: domaincontainer.PredicateTypeSLSAProvenance1,
		KeyRef: signing.KeyRef, OIDCIssuer: signing.OIDCIssuer, FulcioURL: signing.FulcioURL,
		RekorURL: signing.RekorURL, TrustedRootPath: signing.TrustedRootPath, Provenance: prov,
	}
}
