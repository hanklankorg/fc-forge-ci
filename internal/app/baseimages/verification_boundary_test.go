// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Only a typed absence, or the refusal an anonymous read of a missing or
// private package gets from GHCR, is a miss; every other failure stops the
// check before anything is verified.
func TestBaseVerificationBoundary_OnlyAbsenceOrAnAnonymousRefusalIsMissing(t *testing.T) {
	t.Parallel()

	for _, cause := range []error{nil, errs.ErrMissingInput, errs.ErrPermissionDenied, errs.ErrDependencyUnavailable, context.Canceled, context.DeadlineExceeded} {
		in := BaseImageVerifyExistingInput{Flavors: []string{"rust", "go"}, BaseInputID: promoteInput, ExpectedRepository: promoteRepo, ExpectedSource: "https://source.example/repo", ExpectedWorkflow: "base.yml", CosignPublicKey: "fixture.pub"}
		first, second := promoteRepo+":"+promoteInput+"-rust", promoteRepo+":"+promoteInput+"-go"
		registry := &fakeBaseImageRegistry{digests: map[string]string{first: promoteDigest, second: promoteDigest}, resolveErr: map[string]error{second: cause}}
		verifier := &boundaryVerifier{fakeBaseImageVerifier: &fakeBaseImageVerifier{payload: baseLineagePayload(t,
			lineageFields{source: in.ExpectedSource, workflow: in.ExpectedWorkflow, flavor: "rust", baseInputID: promoteInput},
			lineageFields{source: in.ExpectedSource, workflow: in.ExpectedWorkflow, flavor: "go", baseInputID: promoteInput})}}

		var out bytes.Buffer

		result, err := VerifyExistingBaseImages(t.Context(), registry, verifier, &out, in)

		switch {
		case cause == nil:
			if err != nil || !result.AllFound || len(verifier.refs) != 2 || !strings.Contains(out.String(), "already exists and verifies") {
				t.Fatalf("err=%v result=%+v verify=%v out=%s", err, result, verifier.refs, &out)
			}
		case errors.Is(cause, errs.ErrMissingInput), errors.Is(cause, errs.ErrPermissionDenied):
			if err != nil || result.AllFound || result.MissingFlavorsJSON != `["go"]` || len(verifier.refs) != 1 {
				t.Fatalf("missing result=%+v err=%v", result, err)
			}
		default:
			if !errors.Is(err, cause) || len(verifier.refs) != 0 || out.Len() != 0 {
				t.Fatalf("cause=%v err=%v verifier=%v out=%s", cause, err, verifier.refs, &out)
			}
		}
	}
}

func TestBaseVerificationBoundary_AllFlavorsAndIDsPreflight(t *testing.T) {
	t.Parallel()

	for _, change := range []func(*BaseImageVerifyExistingInput){
		func(in *BaseImageVerifyExistingInput) { in.Flavors = []string{"rust", "bad/flavor"} },
		func(in *BaseImageVerifyExistingInput) { in.Flavors = []string{"rust", "rust"} },
		func(in *BaseImageVerifyExistingInput) {
			in.BaseInputID = ""
			in.BaseInputs = []BaseInput{{Flavor: "rust", BaseInputID: promoteInput}}
		},
		func(in *BaseImageVerifyExistingInput) {
			in.BaseInputs = []BaseInput{{Flavor: "rust", BaseInputID: promoteInput}, {Flavor: "go", BaseInputID: "bad"}}
		},
		func(in *BaseImageVerifyExistingInput) {
			in.BaseInputs = []BaseInput{{Flavor: "rust", BaseInputID: promoteInput}, {Flavor: "unused/invalid", BaseInputID: promoteInput}}
		},
		func(in *BaseImageVerifyExistingInput) {
			in.BaseInputs = []BaseInput{{Flavor: "rust", BaseInputID: promoteInput}, {Flavor: "rust", BaseInputID: promoteInput}}
		},
	} {
		in := BaseImageVerifyExistingInput{Flavors: []string{"rust", "go"}, BaseInputID: promoteInput, ExpectedRepository: promoteRepo, ExpectedSource: "https://source.example/repo", ExpectedWorkflow: "base.yml", CosignPublicKey: "fixture.pub"}
		change(&in)

		registry := &fakeBaseImageRegistry{digests: map[string]string{promoteRepo + ":" + promoteInput + "-rust": promoteDigest}}
		verifier := &boundaryVerifier{fakeBaseImageVerifier: &fakeBaseImageVerifier{payload: baseLineagePayload(t, lineageFields{source: in.ExpectedSource, workflow: in.ExpectedWorkflow, flavor: "rust", baseInputID: promoteInput})}}

		var out bytes.Buffer

		_, err := VerifyExistingBaseImages(t.Context(), registry, verifier, &out, in)
		if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "entry 1") || len(registry.resolved) != 0 || len(verifier.refs) != 0 || out.Len() != 0 {
			t.Fatalf("err=%v resolved=%v verifier=%v out=%s", err, registry.resolved, verifier.refs, &out)
		}
	}
}

func TestAuthenticatedBaseVerificationRefusesDeniedInsteadOfRebuilding(t *testing.T) {
	t.Parallel()

	for _, authenticated := range []bool{false, true} {
		in := BaseImageVerifyExistingInput{Authenticated: authenticated, Flavors: []string{"rust"}, BaseInputID: promoteInput, ExpectedRepository: promoteRepo, ExpectedSource: "https://source.example/repo", ExpectedWorkflow: "base.yml", CosignPublicKey: "fixture.pub"}
		tag := promoteRepo + ":" + promoteInput + "-rust"
		registry := &fakeBaseImageRegistry{resolveErr: map[string]error{tag: errs.ErrPermissionDenied}}
		verifier := &boundaryVerifier{fakeBaseImageVerifier: &fakeBaseImageVerifier{}}

		var out bytes.Buffer

		result, err := VerifyExistingBaseImages(t.Context(), registry, verifier, &out, in)
		if authenticated {
			if !errors.Is(err, errs.ErrPermissionDenied) || out.Len() != 0 || result.MissingFlavorsJSON != "" {
				t.Fatalf("authenticated refusal must not become a cache miss: result=%+v err=%v out=%s", result, err, &out)
			}
		} else if err != nil || result.MissingFlavorsJSON != `["rust"]` {
			t.Fatalf("anonymous miss: result=%+v err=%v", result, err)
		}

		if len(verifier.refs) != 0 {
			t.Fatal("denied resolution must not verify evidence")
		}
	}
}

func TestAuthenticatedBaseVerificationReusesOnlyVerifiedEvidence(t *testing.T) {
	t.Parallel()

	in := BaseImageVerifyExistingInput{Authenticated: true, Flavors: []string{"rust"}, BaseInputID: promoteInput, ExpectedRepository: promoteRepo, ExpectedSource: "https://source.example/repo", ExpectedWorkflow: "base.yml", CosignPublicKey: "fixture.pub"}
	tag := promoteRepo + ":" + promoteInput + "-rust"
	registry := &fakeBaseImageRegistry{digests: map[string]string{tag: promoteDigest}}
	verifier := &boundaryVerifier{fakeBaseImageVerifier: &fakeBaseImageVerifier{payload: baseLineagePayload(t, lineageFields{source: in.ExpectedSource, workflow: in.ExpectedWorkflow, flavor: "rust", baseInputID: promoteInput})}}

	result, err := VerifyExistingBaseImages(t.Context(), registry, verifier, &bytes.Buffer{}, in)
	if err != nil || !result.AllFound || result.MissingFlavorsJSON != `[]` || len(verifier.refs) != 1 || !strings.Contains(result.AllImagesJSON, promoteDigest) {
		t.Fatalf("verified reuse: result=%+v err=%v refs=%v", result, err, verifier.refs)
	}
}

func TestCandidatePairBoundary_RejectsIncompleteLaterEntry(t *testing.T) {
	t.Parallel()

	for _, refOnly := range []bool{true, false} {
		first := promoteImage()
		second := first
		second.Flavor = "go"

		second.Tag = promoteRepo + ":go-final"
		if refOnly {
			second.CandidateRef = second.Ref
		} else {
			second.CandidateTag = promoteRepo + ":candidate-go"
		}

		in := promoteInputFor(first, second)
		registry := &fakeBaseImageRegistry{digests: map[string]string{first.Tag: promoteDigest, second.Tag: promoteDigest, promoteRepo + ":candidate-go": promoteDigest}}
		verifier := &boundaryVerifier{fakeBaseImageVerifier: &fakeBaseImageVerifier{payload: baseLineagePayload(t, lineageFields{source: in.ExpectedSource, workflow: in.ExpectedWorkflow, flavor: "rust", baseInputID: promoteInput}, lineageFields{source: in.ExpectedSource, workflow: in.ExpectedWorkflow, flavor: "go", baseInputID: promoteInput})}}

		var out bytes.Buffer

		_, err := PromoteBaseImages(t.Context(), registry, verifier, &out, in)
		if !errors.Is(err, errs.ErrValidation) || len(registry.resolved) != 0 || len(registry.copied) != 0 || len(verifier.refs) != 0 || out.Len() != 0 {
			t.Fatalf("err=%v resolved=%v copies=%v out=%s", err, registry.resolved, registry.copied, &out)
		}
	}
}
