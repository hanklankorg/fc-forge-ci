// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"context"
	"fmt"
	"io"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// BaseParentAuthentication names the one published base a child may build on:
// its exact digest-pinned ref and the authenticated verification of its final
// tag against the pinned trust key, source, workflow, flavor and input ID.
type BaseParentAuthentication struct {
	Verify      BaseImageVerifyExistingInput
	ExpectedRef string
}

// AuthenticateBaseParent is the only legitimate source of a child's
// ExpectedParent. It returns the parent projection solely after an
// authenticated, successful signature, SBOM and SLSA verification of exactly
// one final tag that resolves to ExpectedRef. A child's own parent_base, when
// given in Verify.BaseInputs, is required in the same verified statement.
func AuthenticateBaseParent(ctx context.Context, registry baseImageRegistry, verifier imageEvidenceVerifier, in BaseParentAuthentication) (BaseImageParent, error) {
	var empty BaseImageParent

	verify := in.Verify
	if !verify.Authenticated || len(verify.Flavors) != 1 || len(verify.BaseInputs) != 1 || verify.BaseInputID != "" ||
		verify.BaseInputs[0].Flavor != verify.Flavors[0] || !domaincontainer.ValidDigestPinnedRef(in.ExpectedRef) {
		return empty, fmt.Errorf("base parent authentication requires one authenticated flavor, its input ID and the exact ref: %w", errs.ErrUsage)
	}

	verified, _, err := verifyExistingBaseImages(ctx, registry, verifier, io.Discard, verify)
	if err != nil {
		return empty, err
	}

	// The verifier reports the expectation's own flavor and input ID; only the
	// resolved final tag's digest is new, and it must be the exact parent ref.
	// A missing tag leaves no image, and is refused here too.
	if len(verified) != 1 || verified[0].Ref != in.ExpectedRef {
		return empty, fmt.Errorf("published base differs from exactly one verified parent: %w", errs.ErrValidation)
	}

	return BaseImageParent{Ref: verified[0].Ref, InputID: verified[0].BaseInputID}, nil
}
