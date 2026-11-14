// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

type promotedBaseImage struct {
	Flavor      string `json:"flavor"`
	Tag         string `json:"tag"`
	Ref         string `json:"ref"`
	BaseInputID string `json:"base_input_id"`
}

type baseImageRefOutput struct {
	Flavor      string `json:"flavor"`
	Ref         string `json:"ref"`
	BaseInputID string `json:"base_input_id"`
}

type baseImageInputOutput struct {
	Flavor      string `json:"flavor"`
	BaseInputID string `json:"base_input_id"`
}

// PromoteBaseImages verifies candidate/base evidence, promotes missing final
// immutable tags, and emits the compact JSON outputs consumed by downstream base
// image jobs.
func PromoteBaseImages(ctx context.Context, registry baseImageRegistry, verifier imageEvidenceVerifier, out io.Writer, in BaseImagePromoteInput) (BaseImagePromoteResult, error) {
	if registry == nil {
		return BaseImagePromoteResult{}, fmt.Errorf("base images promote: registry is required: %w", errs.ErrUsage)
	}

	if verifier == nil {
		return BaseImagePromoteResult{}, fmt.Errorf("base images promote: cosign verifier is required: %w", errs.ErrUsage)
	}

	items, baseInputs, err := normalizeBaseImagePromotion(in)
	if err != nil {
		return BaseImagePromoteResult{}, err
	}
	// Evidence and immutable destination checks for every entry precede copies.
	for _, item := range items {
		if err := preflightBaseImagePromotion(ctx, registry, verifier, in, item, baseInputs[item.Flavor].ParentBase); err != nil {
			return BaseImagePromoteResult{}, err
		}
	}

	promoted := make([]promotedBaseImage, 0, len(items))
	for _, item := range items {
		if item.CandidateRef != "" {
			if err := ensureFinalBaseTag(ctx, registry, out, item, digestFromRef(item.Ref)); err != nil {
				return BaseImagePromoteResult{}, err
			}
		}

		promoted = append(promoted, promotedBaseImage{Flavor: item.Flavor, Tag: item.Tag, Ref: item.Ref, BaseInputID: item.BaseInputID})
	}

	return baseImagePromotionResult(promoted)
}

// ValidateBaseImagePromotion validates the complete metadata and mandatory expected
// input map without registry or verifier I/O. The CLI uses it before login.
func ValidateBaseImagePromotion(in BaseImagePromoteInput) error {
	_, _, err := normalizeBaseImagePromotion(in)

	return err
}

func normalizeBaseImagePromotion(in BaseImagePromoteInput) ([]BaseImageMetadata, map[string]BaseInput, error) {
	if err := validateBaseImageCommon(in.ExpectedRepository, in.ExpectedSource, in.ExpectedWorkflow, in.BaseInputID, in.CosignPublicKey); err != nil {
		return nil, nil, err
	}

	if len(in.Images) == 0 {
		return nil, nil, fmt.Errorf("base images promote: all-images-json must contain at least one image: %w", errs.ErrValidation)
	}

	if len(in.BaseInputs) == 0 {
		return nil, nil, fmt.Errorf("base images promote: base-inputs-json must map every promoted flavor: %w", errs.ErrValidation)
	}

	baseInputs, err := baseInputByFlavor(in.BaseInputs)
	if err != nil {
		return nil, nil, err
	}

	items := make([]BaseImageMetadata, 0, len(in.Images))

	destinations := make(map[string]bool, len(in.Images))

	flavors := make(map[string]bool, len(in.Images))
	for idx, image := range in.Images {
		item, err := normalizePromoteBaseImage(image, in.BaseInputID, in.ExpectedRepository)
		if err != nil {
			return nil, nil, fmt.Errorf("base images promote: entry %d: %w", idx, err)
		}

		if destinations[item.Tag] {
			return nil, nil, fmt.Errorf("base images promote: duplicate final destination: %w", errs.ErrValidation)
		}

		if flavors[item.Flavor] {
			return nil, nil, fmt.Errorf("base images promote: duplicate image flavor: %w", errs.ErrValidation)
		}

		expected, exists := baseInputs[item.Flavor]
		if !exists || expected.BaseInputID != item.BaseInputID {
			return nil, nil, fmt.Errorf("base images promote: entry %d: missing or conflicting base-inputs-json expectation: %w", idx, errs.ErrValidation)
		}

		destinations[item.Tag] = true
		flavors[item.Flavor] = true
		items = append(items, item)
	}

	for flavor := range baseInputs {
		if !flavors[flavor] {
			return nil, nil, fmt.Errorf("base images promote: base-inputs-json flavor %q has no target image: %w", flavor, errs.ErrValidation)
		}
	}

	return items, baseInputs, nil
}

func preflightBaseImagePromotion(ctx context.Context, registry baseImageRegistry, verifier imageEvidenceVerifier, in BaseImagePromoteInput, item BaseImageMetadata, parent *BaseImageParent) error {
	digest := digestFromRef(item.Ref)
	if err := verifyBaseImageEvidence(ctx, verifier, io.Discard, item.Ref, in.CosignPublicKey, baseImageLineageExpectation{
		Source: in.ExpectedSource, Workflow: in.ExpectedWorkflow, Flavor: item.Flavor, BaseInputID: item.BaseInputID,
		ParentBase: parent,
	}, 3); err != nil {
		return err
	}

	if item.CandidateTag != "" {
		if err := verifyBaseTagDigest(ctx, registry, item.CandidateTag, digest, in.ExpectedRepository); err != nil {
			return err
		}
	}

	finalDigest, err := registry.ResolveDigest(ctx, item.Tag)
	if errors.Is(err, errs.ErrMissingInput) && item.CandidateRef != "" {
		return nil
	}

	if err != nil {
		return fmt.Errorf("base images promote: resolve final tag: %w", err)
	}

	if finalDigest != digest {
		return fmt.Errorf("base images promote: immutable final base tag already exists with a different digest: %w", errs.ErrValidation)
	}

	return nil
}

// ensureFinalBaseTag copies the candidate to the immutable final tag when the
// final tag is absent, and rejects promotion when an existing final tag (or
// the tag just copied) does not resolve to the candidate digest.
func ensureFinalBaseTag(ctx context.Context, registry baseImageRegistry, out io.Writer, item BaseImageMetadata, digest string) error {
	finalDigest, err := registry.ResolveDigest(ctx, item.Tag)
	if err == nil {
		if finalDigest != digest {
			return fmt.Errorf("base images promote: immutable final base tag already exists with a different digest\n  tag:       %s\n  existing:  %s\n  candidate: %s: %w", item.Tag, finalDigest, digest, errs.ErrValidation)
		}

		return nil
	}

	if !errors.Is(err, errs.ErrMissingInput) {
		return fmt.Errorf("base images promote: resolve immutable final base tag %s: %w", item.Tag, err)
	}

	_, _ = fmt.Fprintf(out, "Promoting base image: flavor=%s digest=%s final=%s\n", item.Flavor, digest, item.Tag)
	if copyErr := registry.CopyTag(ctx, item.CandidateRef, item.Tag); copyErr != nil {
		return copyErr
	}

	finalDigest, err = registry.ResolveDigest(ctx, item.Tag)
	if err != nil {
		return err
	}

	if finalDigest != digest {
		return fmt.Errorf("base images promote: final base digest changed during promotion: %w", errs.ErrValidation)
	}

	return nil
}

func baseImagePromotionResult(promoted []promotedBaseImage) (BaseImagePromoteResult, error) {
	slices.SortFunc(promoted, func(a, b promotedBaseImage) int { return strings.Compare(a.Flavor, b.Flavor) })

	refs := make([]baseImageRefOutput, 0, len(promoted))
	inputs := make([]baseImageInputOutput, 0, len(promoted))

	uniqueIDs := make(map[string]bool, len(promoted))
	for _, image := range promoted {
		refs = append(refs, baseImageRefOutput{Flavor: image.Flavor, Ref: image.Ref, BaseInputID: image.BaseInputID})
		inputs = append(inputs, baseImageInputOutput{Flavor: image.Flavor, BaseInputID: image.BaseInputID})
		uniqueIDs[image.BaseInputID] = true
	}

	refsJSON, err := compactJSON(refs)
	if err != nil {
		return BaseImagePromoteResult{}, err
	}

	inputsJSON, err := compactJSON(inputs)
	if err != nil {
		return BaseImagePromoteResult{}, err
	}

	baseInputID := ""

	if len(uniqueIDs) == 1 {
		for id := range uniqueIDs {
			baseInputID = id
		}
	}

	return BaseImagePromoteResult{BaseInputID: baseInputID, BaseImagesJSON: refsJSON, BaseInputIDsJSON: inputsJSON}, nil
}
