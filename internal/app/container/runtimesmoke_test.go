// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestRuntimeSmokeEvidenceIsStrictAndBounded(t *testing.T) {
	t.Parallel()

	expected := domaincontainer.RuntimeSmokeReceipt{Source: strings.Repeat("a", 40), Target: "go", Arch: "amd64", Ref: "registry.example/owner/runtime@sha256:" + strings.Repeat("b", 64)}

	body, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}

	for name, receipt := range map[string]string{
		"valid": string(body), "empty": `{}`, "null": `null`, "trailing": string(body) + `{}`,
		"unknown field":    strings.TrimSuffix(string(body), "}") + `,"untrusted":true}`,
		"wrong field type": `{"source":3}`, "broken": `{`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			file := filepath.Join(t.TempDir(), "receipt.json")
			if err := os.WriteFile(file, []byte(receipt), 0o600); err != nil {
				t.Fatal(err)
			}

			err := VerifyRuntimeSmoke(t.Context(), file, expected)
			if (err == nil) != (name == "valid") {
				t.Fatalf("%s: verification error = %v", name, err)
			}
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := VerifyRuntimeSmoke(ctx, "must-not-read", expected); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must precede file access: %v", err)
	}

	if err := VerifyRuntimeSmoke(t.Context(), "must-not-read", domaincontainer.RuntimeSmokeReceipt{}); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("invalid expectations must precede file access: %v", err)
	}

	if err := VerifyRuntimeSmoke(t.Context(), t.TempDir(), expected); err == nil {
		t.Fatal("a directory is not smoke evidence")
	}
}
