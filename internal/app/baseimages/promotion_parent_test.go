// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func promotionParentInput() BaseImagePromoteInput {
	id := strings.Repeat("a", 64)
	ref := promoteRepo + "@" + promoteDigest

	return BaseImagePromoteInput{
		Images: []BaseImageMetadata{{Flavor: "child", Tag: promoteRepo + ":final", Ref: ref, CandidateTag: promoteRepo + ":candidate", CandidateRef: ref, BaseInputID: id}},
		BaseInputs: []BaseInput{{Flavor: "child", BaseInputID: id, ParentBase: &BaseImageParent{
			Ref: "other.example/shared/parent@sha256:" + strings.Repeat("b", 64), InputID: strings.Repeat("c", 64),
		}}},
		ExpectedRepository: promoteRepo, ExpectedSource: "https://forge.example/acme/app", ExpectedWorkflow: "bases.yml", CosignPublicKey: "key.pub",
	}
}

// These are application/port tests, not cryptographic acceptance tests. The
// evidence recorder stands in for cosign's already-verified statement output.
func TestPromotionParentSameStatement(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"correct", "wrong ref", "wrong id", "missing", "split", "no map absent", "no map present", "explicit no expectation"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			in := promotionParentInput()
			params := parentBindingParams()
			statements := []map[string]any{params}

			switch mode {
			case "wrong ref":
				params["parent_base"].(map[string]any)["ref"] = "other.example/wrong@sha256:" + strings.Repeat("b", 64)
			case "wrong id":
				params["parent_base"].(map[string]any)["input_id"] = strings.Repeat("d", 64)
			case "missing", "no map absent":
				delete(params, "parent_base")
			case "split":
				delete(params, "parent_base")

				other := parentBindingParams()
				other["base_input_id"] = strings.Repeat("d", 64)
				statements = append(statements, other)
			}

			if strings.HasPrefix(mode, "no map") {
				in.BaseInputs = nil
			}

			if mode == "explicit no expectation" {
				in.BaseInputs[0].ParentBase = nil
			}

			image := in.Images[0]
			registry := &fakeBaseImageRegistry{digests: map[string]string{image.CandidateTag: promoteDigest}}
			verifier := newEvidenceRecorder(t, t.Context(), map[string][]byte{image.Ref: parentBindingPayload(t, statements...)}, 0, nil)
			result, err := PromoteBaseImages(t.Context(), registry, verifier, io.Discard, in)

			// The map is mandatory: there is no legacy path without one.
			wantError := mode == "wrong ref" || mode == "wrong id" || mode == "missing" || mode == "split" || strings.HasPrefix(mode, "no map")
			if wantError {
				if !errors.Is(err, errs.ErrValidation) || len(registry.copied) != 0 || result.BaseImagesJSON != "" {
					t.Fatalf("err=%v copies=%v result=%+v", err, registry.copied, result)
				}
			} else if err != nil || len(registry.copied) != 1 || result.BaseInputID != image.BaseInputID {
				t.Fatalf("err=%v copies=%v result=%+v", err, registry.copied, result)
			}
		})
	}
}

func TestPromotionExpectedInputsPreflight(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"empty", "missing", "duplicate", "conflict", "shared conflict", "bad own id", "bad flavor", "bad ref", "empty ref", "bad parent id", "empty parent id", "unused malformed", "unknown valid", "unknown valid no parent", "duplicate image", "later metadata"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			in := promotionParentInput()

			switch mode {
			case "empty":
				in.BaseInputs = []BaseInput{}
			case "missing":
				in.BaseInputs[0].Flavor = "other"
			case "duplicate":
				in.BaseInputs = append(in.BaseInputs, in.BaseInputs[0])
			case "conflict":
				in.BaseInputs[0].BaseInputID = strings.Repeat("d", 64)
			case "shared conflict":
				in.BaseInputID = strings.Repeat("d", 64)
			case "bad own id":
				in.BaseInputs[0].BaseInputID = "bad"
			case "bad flavor":
				in.BaseInputs[0].Flavor = "bad/flavor"
			case "bad ref":
				in.BaseInputs[0].ParentBase.Ref = "other.example/shared/parent:mutable"
			case "empty ref":
				in.BaseInputs[0].ParentBase.Ref = ""
			case "bad parent id":
				in.BaseInputs[0].ParentBase.InputID = "bad"
			case "empty parent id":
				in.BaseInputs[0].ParentBase.InputID = ""
			case "unused malformed":
				in.BaseInputs = append(in.BaseInputs, BaseInput{Flavor: "unused", BaseInputID: "bad"})
			case "unknown valid":
				extra := in.BaseInputs[0]
				extra.Flavor = "other"
				in.BaseInputs = append(in.BaseInputs, extra)
			case "unknown valid no parent":
				in.BaseInputs = append(in.BaseInputs, BaseInput{Flavor: "unused", BaseInputID: strings.Repeat("d", 64)})
			case "duplicate image", "later metadata":
				other := in.Images[0]

				other.Tag = promoteRepo + ":second"
				if mode == "later metadata" {
					other.Ref = "bad"
				}

				in.Images = append(in.Images, other)
			}

			registry := &fakeBaseImageRegistry{}

			verifier := newEvidenceRecorder(t, t.Context(), nil, 0, nil)
			if err := ValidateBaseImagePromotion(in); !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("pre-login validation: %v", err)
			}

			_, err := PromoteBaseImages(t.Context(), registry, verifier, io.Discard, in)
			if !errors.Is(err, errs.ErrValidation) || len(registry.resolved) != 0 || len(registry.copied) != 0 || len(verifier.calls) != 0 {
				t.Fatalf("err=%v registry=%+v verifier=%v", err, registry, verifier.calls)
			}
		})
	}
}

func TestPromotionExactInputMapBatch(t *testing.T) {
	t.Parallel()

	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "missing"}[complete], func(t *testing.T) {
			t.Parallel()

			in := promotionParentInput()
			second := in.Images[0]
			second.Flavor, second.Tag, second.CandidateTag = "other", promoteRepo+":other", promoteRepo+":candidate-other"

			in.Images = append(in.Images, second)
			if complete {
				expected := in.BaseInputs[0]
				expected.Flavor = second.Flavor
				in.BaseInputs = append(in.BaseInputs, expected)
			}

			params := parentBindingParams()
			params["flavor"] = second.Flavor
			registry := &fakeBaseImageRegistry{digests: map[string]string{in.Images[0].CandidateTag: promoteDigest, second.CandidateTag: promoteDigest}}
			verifier := newEvidenceRecorder(t, t.Context(), map[string][]byte{
				second.Ref: parentBindingPayload(t, parentBindingParams(), params),
			}, 0, nil)

			result, err := PromoteBaseImages(t.Context(), registry, verifier, io.Discard, in)
			if complete {
				if err != nil || len(registry.copied) != 2 || !strings.Contains(result.BaseImagesJSON, "other") {
					t.Fatalf("err=%v copies=%v result=%+v", err, registry.copied, result)
				}
			} else if !errors.Is(err, errs.ErrValidation) || len(registry.resolved) != 0 || len(registry.copied) != 0 || len(verifier.calls) != 0 {
				t.Fatalf("err=%v registry=%+v verifier=%v", err, registry, verifier.calls)
			}
		})
	}
}

func TestPromotionParentLaterMismatchPreventsAllCopies(t *testing.T) {
	t.Parallel()

	in := promotionParentInput()
	second := in.Images[0]
	second.Flavor, second.Tag, second.CandidateTag = "second", promoteRepo+":second", promoteRepo+":candidate-second"
	second.Ref = promoteRepo + "@sha256:" + strings.Repeat("f", 64)
	second.CandidateRef = second.Ref
	second.BaseInputID = strings.Repeat("e", 64)
	in.Images = append(in.Images, second)
	in.BaseInputs = append(in.BaseInputs, BaseInput{Flavor: second.Flavor, BaseInputID: second.BaseInputID, ParentBase: in.BaseInputs[0].ParentBase})
	params := parentBindingParams()
	params["flavor"], params["base_input_id"] = second.Flavor, second.BaseInputID
	delete(params, "parent_base")

	registry := &fakeBaseImageRegistry{digests: map[string]string{in.Images[0].CandidateTag: promoteDigest}}
	verifier := newEvidenceRecorder(t, t.Context(), map[string][]byte{
		in.Images[0].Ref: parentBindingPayload(t, parentBindingParams()), second.Ref: parentBindingPayload(t, params),
	}, 0, nil)

	_, err := PromoteBaseImages(t.Context(), registry, verifier, io.Discard, in)
	if !errors.Is(err, errs.ErrValidation) || len(registry.copied) != 0 {
		t.Fatalf("err=%v copies=%v", err, registry.copied)
	}
}
