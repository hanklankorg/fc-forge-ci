// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestAuthenticateBaseParent(t *testing.T) {
	t.Parallel()

	ownID := strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("e", 64)
	repository := "forge.example/acme/child"
	ref := repository + "@" + digest
	tag := repository + ":" + ownID + "-child"
	errKey := errors.New("signature does not verify with the pinned key")

	for _, mode := range []string{"root", "child", "not found", "wrong key", "ref drift", "mutable ref"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			parent := &BaseImageParent{Ref: "other.example/shared/parent@sha256:" + strings.Repeat("b", 64), InputID: strings.Repeat("c", 64)}
			in := BaseParentAuthentication{ExpectedRef: ref, Verify: BaseImageVerifyExistingInput{
				Authenticated: true, Flavors: []string{"child"}, BaseInputs: []BaseInput{{Flavor: "child", BaseInputID: ownID, ParentBase: parent}},
				ExpectedRepository: repository, ExpectedSource: "https://forge.example/acme/app", ExpectedWorkflow: "bases.yml", CosignPublicKey: "key.pub",
			}}
			registry := &fakeBaseImageRegistry{digests: map[string]string{tag: digest}}
			params := parentBindingParams()
			failAt, portErr := 0, error(nil)

			switch mode {
			case "root":
				in.Verify.BaseInputs[0].ParentBase = nil

				delete(params, "parent_base")
			case "not found":
				delete(registry.digests, tag)
			case "wrong key":
				failAt, portErr = 1, errKey
			case "ref drift":
				in.ExpectedRef = repository + "@sha256:" + strings.Repeat("f", 64)
			case "mutable ref":
				in.ExpectedRef = repository + ":latest"
			}

			verifier := newEvidenceRecorder(t, t.Context(), map[string][]byte{ref: parentBindingPayload(t, params)}, failAt, portErr)
			got, err := AuthenticateBaseParent(t.Context(), registry, verifier, in)

			if mode == "root" || mode == "child" {
				if err != nil || got != (BaseImageParent{Ref: ref, InputID: ownID}) {
					t.Fatalf("parent=%+v err=%v", got, err)
				}

				return
			}

			if err == nil || got != (BaseImageParent{}) {
				t.Fatalf("authenticated a refused parent: %+v", got)
			}

			if !errors.Is(err, errs.ErrValidation) && !errors.Is(err, errs.ErrUsage) {
				t.Fatalf("want validation or usage refusal: %v", err)
			}

			if mode == "mutable ref" && (!errors.Is(err, errs.ErrUsage) || len(registry.resolved) != 0) {
				t.Fatalf("want a usage refusal before registry I/O: %v resolved=%v", err, registry.resolved)
			}

			if len(registry.copied) != 0 {
				t.Fatal("authentication mutated the registry")
			}
		})
	}
}
