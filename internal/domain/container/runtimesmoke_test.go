// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestRuntimeSmokeReceiptBindsEveryIdentity(t *testing.T) {
	t.Parallel()

	expected := RuntimeSmokeReceipt{Source: strings.Repeat("a", 40), Target: "go", Arch: "amd64", Ref: "registry.example/owner/runtime@sha256:" + strings.Repeat("b", 64)}
	if err := VerifyRuntimeSmokeReceipt(expected, expected); err != nil {
		t.Fatal(err)
	}

	for name, change := range map[string]func(*RuntimeSmokeReceipt){
		"source": func(in *RuntimeSmokeReceipt) { in.Source = strings.Repeat("c", 40) },
		"target": func(in *RuntimeSmokeReceipt) { in.Target = "signer" },
		"arch":   func(in *RuntimeSmokeReceipt) { in.Arch = "arm64" },
		"digest": func(in *RuntimeSmokeReceipt) {
			in.Ref = "registry.example/owner/runtime@sha256:" + strings.Repeat("c", 64)
		},
		"repository": func(in *RuntimeSmokeReceipt) {
			in.Ref = "registry.example/other/runtime@sha256:" + strings.Repeat("b", 64)
		},
		"empty": func(in *RuntimeSmokeReceipt) { *in = RuntimeSmokeReceipt{} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			receipt := expected
			change(&receipt)

			if err := VerifyRuntimeSmokeReceipt(receipt, expected); !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("mismatched receipt = %v, want validation refusal", err)
			}
		})
	}

	for name, change := range map[string]func(*RuntimeSmokeReceipt){
		"source": func(in *RuntimeSmokeReceipt) { in.Source = "main" },
		"target": func(in *RuntimeSmokeReceipt) { in.Target = "../go" },
		"arch":   func(in *RuntimeSmokeReceipt) { in.Arch = "riscv64" },
		"tag":    func(in *RuntimeSmokeReceipt) { in.Ref = "registry.example/owner/runtime:latest" },
	} {
		t.Run("invalid expectation/"+name, func(t *testing.T) {
			t.Parallel()

			invalid := expected
			change(&invalid)

			if err := VerifyRuntimeSmokeReceipt(invalid, invalid); !errors.Is(err, errs.ErrUsage) {
				t.Fatalf("invalid expectations = %v, want usage refusal", err)
			}
		})
	}

	sha256Expected := expected
	sha256Expected.Source, sha256Expected.Arch = strings.Repeat("d", 64), "arm64"

	if err := VerifyRuntimeSmokeReceipt(sha256Expected, sha256Expected); err != nil {
		t.Fatalf("full SHA-256 source and native arm64 must work: %v", err)
	}
}
