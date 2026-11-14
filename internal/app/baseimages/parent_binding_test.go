// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

func parentBindingParams() map[string]any {
	return map[string]any{
		"source": "https://forge.example/acme/app", "workflow": "bases.yml",
		"flavor": "child", "base_input_id": strings.Repeat("a", 64),
		"parent_base": map[string]any{"ref": "other.example/shared/parent@sha256:" + strings.Repeat("b", 64), "input_id": strings.Repeat("c", 64)},
	}
}

func parentBindingPayload(t *testing.T, params ...map[string]any) []byte {
	t.Helper()

	envelopes := make([]map[string]string, 0, len(params))
	for _, fields := range params {
		statement, err := json.Marshal(map[string]any{
			"predicateType": provenance.PredicateTypeV1,
			"predicate":     map[string]any{"buildDefinition": map[string]any{"externalParameters": fields}},
		})
		if err != nil {
			t.Fatal(err)
		}

		envelopes = append(envelopes, map[string]string{"payload": base64.StdEncoding.EncodeToString(statement)})
	}

	body, err := json.Marshal(envelopes)
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func TestParentBindingMatchesOneStatement(t *testing.T) {
	t.Parallel()

	expected := baseImageLineageExpectation{
		Source: "https://forge.example/acme/app", Workflow: "bases.yml", Flavor: "child", BaseInputID: strings.Repeat("a", 64),
		ParentBase: &BaseImageParent{Ref: "other.example/shared/parent@sha256:" + strings.Repeat("b", 64), InputID: strings.Repeat("c", 64)},
	}

	type mismatchCase struct {
		name   string
		mutate func(map[string]any)
	}

	cases := make([]mismatchCase, 0, 21)

	cases = append(cases,
		mismatchCase{"missing parent", func(p map[string]any) { delete(p, "parent_base") }},
		mismatchCase{"null parent", func(p map[string]any) { p["parent_base"] = nil }},
		mismatchCase{"string parent", func(p map[string]any) { p["parent_base"] = "parent" }},
		mismatchCase{"array parent", func(p map[string]any) { p["parent_base"] = []any{} }},
		mismatchCase{"missing ref", func(p map[string]any) { delete(p["parent_base"].(map[string]any), "ref") }},
		mismatchCase{"wrong ref", func(p map[string]any) {
			p["parent_base"].(map[string]any)["ref"] = "other.example/wrong@sha256:" + strings.Repeat("b", 64)
		}},
		mismatchCase{"missing parent id", func(p map[string]any) { delete(p["parent_base"].(map[string]any), "input_id") }},
		mismatchCase{"wrong parent id", func(p map[string]any) { p["parent_base"].(map[string]any)["input_id"] = strings.Repeat("d", 64) }},
		mismatchCase{"malformed parent id", func(p map[string]any) { p["parent_base"].(map[string]any)["input_id"] = 42 }},
	)
	for _, key := range []string{"source", "workflow", "flavor", "base_input_id"} {
		cases = append(cases,
			mismatchCase{"wrong own " + key, func(p map[string]any) { p[key] = "wrong" }},
			mismatchCase{"missing own " + key, func(p map[string]any) { delete(p, key) }},
			mismatchCase{"malformed own " + key, func(p map[string]any) { p[key] = 42 }},
		)
	}

	if err := baseLineageAttestationMatches(parentBindingPayload(t, parentBindingParams()), expected); err != nil {
		t.Fatal(err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := parentBindingParams()
			tc.mutate(params)

			if err := baseLineageAttestationMatches(parentBindingPayload(t, params), expected); !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("got %v, want validation refusal", err)
			}
		})
	}

	legacy := parentBindingParams()
	delete(legacy, "parent_base")

	noExpectation := expected

	noExpectation.ParentBase = nil
	for _, params := range []map[string]any{legacy, parentBindingParams()} {
		if err := baseLineageAttestationMatches(parentBindingPayload(t, params), noExpectation); err != nil {
			t.Fatal(err)
		}
	}

	parentOnly := parentBindingParams()

	parentOnly["base_input_id"] = strings.Repeat("d", 64)
	if err := baseLineageAttestationMatches(parentBindingPayload(t, legacy, parentOnly), expected); err == nil {
		t.Fatal("combined fields from separate statements")
	}
}

func TestVerifyExistingParentBinding(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"warm valid", "legacy fails expectation", "invalid ref", "invalid id", "invalid empty ref", "invalid empty id", "missing tag"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			parent := &BaseImageParent{Ref: "other.example/shared/parent@sha256:" + strings.Repeat("b", 64), InputID: strings.Repeat("c", 64)}
			ownID := strings.Repeat("a", 64)
			in := BaseImageVerifyExistingInput{
				Flavors: []string{"child"}, BaseInputs: []BaseInput{{Flavor: "child", BaseInputID: ownID, ParentBase: parent}},
				ExpectedRepository: "forge.example/acme/child", ExpectedSource: "https://forge.example/acme/app", ExpectedWorkflow: "bases.yml", CosignPublicKey: "key.pub",
			}
			tag := in.ExpectedRepository + ":" + ownID + "-child"
			registry := &fakeBaseImageRegistry{digests: map[string]string{tag: "sha256:" + strings.Repeat("e", 64)}}
			params := parentBindingParams()

			switch mode {
			case "legacy fails expectation":
				delete(params, "parent_base")
			case "invalid ref":
				parent.Ref = "other.example/shared/parent:mutable"
			case "invalid id":
				parent.InputID = "bad"
			case "invalid empty ref":
				parent.Ref = ""
			case "invalid empty id":
				parent.InputID = ""
			case "missing tag":
				delete(registry.digests, tag)
			}

			ctx := t.Context()
			ref := in.ExpectedRepository + "@sha256:" + strings.Repeat("e", 64)
			verifier := newEvidenceRecorder(t, ctx, map[string][]byte{ref: parentBindingPayload(t, params)}, 0, nil)
			result, err := VerifyExistingBaseImages(ctx, registry, verifier, io.Discard, in)

			wantError := mode == "legacy fails expectation" || strings.HasPrefix(mode, "invalid")
			if (err != nil) != wantError {
				t.Fatalf("result=%+v err=%v", result, err)
			}

			if wantError && !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("want validation error: %v", err)
			}

			if strings.HasPrefix(mode, "invalid") && len(registry.resolved) != 0 {
				t.Fatal("registry called before expectation validation")
			}

			if strings.HasPrefix(mode, "invalid") || mode == "missing tag" {
				if len(verifier.calls) != 0 {
					t.Fatal("unexpected evidence I/O")
				}
			} else if !reflect.DeepEqual(verifier.calls, evidenceRequests(ref, in.CosignPublicKey)) {
				t.Fatalf("evidence order/requests = %#v", verifier.calls)
			}

			if len(registry.copied) != 0 {
				t.Fatal("verification mutated registry")
			}

			if mode == "warm valid" && (!result.AllFound || strings.Contains(result.AllImagesJSON, "parent_base")) {
				t.Fatalf("unexpected output: %+v", result)
			}

			if mode == "missing tag" && (result.AllFound || result.MissingFlavorsJSON != `["child"]`) {
				t.Fatalf("missing behavior changed: %+v", result)
			}
		})
	}
}
