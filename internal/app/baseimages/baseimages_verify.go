// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

type verifiedBaseImage struct {
	Flavor       string `json:"flavor"`
	Tag          string `json:"tag"`
	Ref          string `json:"ref"`
	CandidateTag string `json:"candidate_tag"`
	CandidateRef string `json:"candidate_ref"`
	BaseInputID  string `json:"base_input_id"`
}

// VerifyExistingBaseImages checks the immutable final base-image tags for the
// requested flavors. Missing tags are reported as cache misses, matching the
// previous workflow behavior; a tag that exists must verify with signature, SBOM
// attestation, and SLSA lineage before it is returned.
func VerifyExistingBaseImages(ctx context.Context, resolver baseImageRegistry, verifier imageEvidenceVerifier, out io.Writer, in BaseImageVerifyExistingInput) (BaseImageVerifyExistingResult, error) { //nolint:cyclop // complete input, resolution and evidence phases precede publication.
	if err := validateVerifyExistingBaseInput(resolver, verifier, in); err != nil {
		return BaseImageVerifyExistingResult{}, err
	}

	baseInputs, err := baseInputByFlavor(in.BaseInputs)
	if err != nil {
		return BaseImageVerifyExistingResult{}, err
	}

	seen := make(map[string]bool, len(in.Flavors))
	for idx, flavor := range in.Flavors {
		if seen[flavor] {
			return BaseImageVerifyExistingResult{}, fmt.Errorf("base images verify: duplicate flavor at entry %d: %w", idx, errs.ErrValidation)
		}

		if _, idErr := existingBaseInputID(baseInputs, flavor, in.BaseInputID); idErr != nil {
			return BaseImageVerifyExistingResult{}, fmt.Errorf("base images verify: entry %d: %w", idx, idErr)
		}

		seen[flavor] = true
	}

	var progress bytes.Buffer

	existing := make([]verifiedBaseImage, 0, len(in.Flavors))
	missing := make([]string, 0)

	for _, flavor := range in.Flavors {
		baseInputID, _ := existingBaseInputID(baseInputs, flavor, in.BaseInputID)
		tag := fmt.Sprintf("%s:%s-%s", in.ExpectedRepository, baseInputID, flavor)

		digest, resolveErr := resolver.ResolveDigest(ctx, tag)
		// A missing local dependency (for example an explicit auth file) can
		// carry both causes. Only remote absence is a cache miss.
		if errors.Is(resolveErr, errs.ErrMissingInput) && !errors.Is(resolveErr, errs.ErrDependencyUnavailable) {
			missing = append(missing, flavor)

			fmt.Fprintf(&progress, "Base image missing and will be built if needed: %s\n", tag)

			continue
		}

		// GHCR cannot distinguish absent from private during anonymous
		// discovery. An explicit credential refusal, however, is a failure,
		// never an invitation to rebuild or change package visibility.
		if errors.Is(resolveErr, errs.ErrPermissionDenied) && !in.Authenticated {
			missing = append(missing, flavor)

			fmt.Fprintf(&progress, "Base image not readable anonymously and will be built if needed: %s\n"+
				"    The registry refused an anonymous read: the package %s does not exist yet or is private.\n"+
				"    For private bases, supply --auth-file so digest and signed-evidence reads authenticate.\n", tag, in.ExpectedRepository)

			continue
		}

		if resolveErr != nil {
			return BaseImageVerifyExistingResult{}, fmt.Errorf("resolve base image %s: %w", tag, resolveErr)
		}

		if !domaincontainer.ValidDigest(digest) {
			return BaseImageVerifyExistingResult{}, fmt.Errorf("base image resolver returned an invalid digest: %w", errs.ErrValidation)
		}

		existing = append(existing, verifiedBaseImage{Flavor: flavor, Tag: tag, Ref: in.ExpectedRepository + "@" + digest, BaseInputID: baseInputID})
	}
	// Finish resolution before any evidence check or success diagnostic.
	for _, image := range existing {
		if verifyErr := verifyOneExistingBaseImage(ctx, verifier, &progress, in, image, baseInputs[image.Flavor].ParentBase); verifyErr != nil {
			return BaseImageVerifyExistingResult{}, verifyErr
		}
	}

	allImagesJSON, err := marshalSortedBaseImages(existing)
	if err != nil {
		return BaseImageVerifyExistingResult{}, err
	}

	slices.Sort(missing)

	missingJSON, err := compactJSON(missing)
	if err != nil {
		return BaseImageVerifyExistingResult{}, err
	}

	if _, err := io.Copy(out, &progress); err != nil {
		return BaseImageVerifyExistingResult{}, err
	}

	return BaseImageVerifyExistingResult{AllFound: len(missing) == 0, AllImagesJSON: allImagesJSON, MissingFlavorsJSON: missingJSON}, nil
}

func validateVerifyExistingBaseInput(resolver baseImageRegistry, verifier imageEvidenceVerifier, in BaseImageVerifyExistingInput) error {
	if resolver == nil {
		return fmt.Errorf("base images verify: registry resolver is required: %w", errs.ErrUsage)
	}

	if verifier == nil {
		return fmt.Errorf("base images verify: cosign verifier is required: %w", errs.ErrUsage)
	}

	if err := validateBaseImageCommon(in.ExpectedRepository, in.ExpectedSource, in.ExpectedWorkflow, in.BaseInputID, in.CosignPublicKey); err != nil {
		return err
	}

	if len(in.Flavors) == 0 {
		return fmt.Errorf("base images verify: flavors file contains no flavors: %w", errs.ErrValidation)
	}

	return nil
}

// existingBaseInputID picks the per-flavor base input ID, falling back to the
// shared one, and validates both the flavor and the ID.
func existingBaseInputID(baseInputs map[string]BaseInput, flavor, fallback string) (string, error) {
	if !baseImageFlavorRE.MatchString(flavor) {
		return "", fmt.Errorf("base images verify: invalid flavor name: %s: %w", flavor, errs.ErrValidation)
	}

	baseInputID := baseInputs[flavor].BaseInputID
	if baseInputID == "" {
		baseInputID = fallback
	}

	if !domaincontainer.ValidSHA256Hex(baseInputID) {
		return "", fmt.Errorf("base images verify: missing or invalid base input ID for flavor: %s: %w", flavor, errs.ErrValidation)
	}

	return baseInputID, nil
}

// verifyOneExistingBaseImage verifies evidence after all tag lookups succeeded.
func verifyOneExistingBaseImage(ctx context.Context, verifier imageEvidenceVerifier, out io.Writer, in BaseImageVerifyExistingInput, image verifiedBaseImage, parent *BaseImageParent) error {
	if err := verifyBaseImageEvidence(ctx, verifier, out, image.Ref, in.CosignPublicKey, baseImageLineageExpectation{
		Source: in.ExpectedSource, Workflow: in.ExpectedWorkflow, Flavor: image.Flavor, BaseInputID: image.BaseInputID,
		ParentBase: parent,
	}, 1); err != nil {
		return fmt.Errorf("base images verify: final base tag exists but does not have the expected signature and attestation: %s: %w", image.Tag, err)
	}

	_, _ = fmt.Fprintf(out, "Base image already exists and verifies: %s\n", image.Ref)

	return nil
}

func baseInputByFlavor(inputs []BaseInput) (map[string]BaseInput, error) {
	byFlavor := make(map[string]BaseInput, len(inputs))
	for idx, input := range inputs {
		if !baseImageFlavorRE.MatchString(input.Flavor) || !domaincontainer.ValidSHA256Hex(input.BaseInputID) {
			return nil, fmt.Errorf("base images verify: base-inputs-json entry %d must include flavor and sha256 base_input_id: %w", idx, errs.ErrValidation)
		}

		if _, exists := byFlavor[input.Flavor]; exists {
			return nil, fmt.Errorf("base images verify: duplicate base-input flavor at entry %d: %w", idx, errs.ErrValidation)
		}

		if parent := input.ParentBase; parent != nil {
			if !domaincontainer.ValidDigestPinnedRef(parent.Ref) || !domaincontainer.ValidSHA256Hex(parent.InputID) {
				return nil, fmt.Errorf("base images verify: base-inputs-json entry %d parent_base must include digest-pinned ref and sha256 input_id: %w", idx, errs.ErrValidation)
			}
		}

		byFlavor[input.Flavor] = input
	}

	return byFlavor, nil
}

func marshalSortedBaseImages(images []verifiedBaseImage) (string, error) {
	slices.SortFunc(images, func(a, b verifiedBaseImage) int { return strings.Compare(a.Flavor, b.Flavor) })

	return compactJSON(images)
}
