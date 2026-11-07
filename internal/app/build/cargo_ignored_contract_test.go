// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Earlier executables cannot supply execution evidence for an ignored-only
// contract executable, even when Cargo itself exits successfully.
func TestCargoIgnoredContractCannotBorrowEarlierPolicyReports(t *testing.T) {
	const selection = `{"runs":[
 {"kind":"package-test","package":"nanolinter-ci","name":"basegraph_provenance","gate":"policy"},
 {"kind":"package-test","package":"nanolinter-ci","name":"go_parity","gate":"policy"},
 {"kind":"package-test","package":"nanolinter-ci","name":"imageplan_contract","gate":"policy"},
 {"kind":"package-test","package":"nanolinter-ci","name":"release_plan","gate":"policy"}
]}`

	calls := 0
	tool := selectionTool(func(_ context.Context, in appbuild.GoRunInput) error {
		calls++

		passed, ignored := 5, 0

		switch calls {
		case 2:
			passed = 11
		case 3:
			passed, ignored = 0, 3
		case 4:
			t.Fatal("continued after missing contract execution evidence")
		}

		_, _ = fmt.Fprintf(in.Stdout, "running %d tests\ntest result: ok. %d passed; 0 failed; %d ignored; 0 measured; 0 filtered out; finished in 0.00s\n", passed+ignored, passed, ignored)

		return nil
	})

	var receipt bytes.Buffer

	err := appbuild.CargoTest(t.Context(), tool, &receipt, io.Discard, appbuild.CargoTestInput{SelectionJSON: selection})
	if !errors.Is(err, errs.ErrValidation) || calls != 3 {
		t.Fatalf("ignored-only contract admitted: calls=%d err=%v", calls, err)
	}

	if strings.Count(receipt.String(), "Selected policy tests: result=pass enforcement=true") != 2 ||
		!strings.Contains(receipt.String(), "Selected policy tests: result=failure enforcement=true") ||
		strings.Contains(receipt.String(), "result=findings") {
		t.Fatalf("policy execution receipts lost: %s", &receipt)
	}
}

func TestCargoExplicitIgnoredPolicyRequiresExecutedContracts(t *testing.T) {
	const selection = `{"runs":[{"kind":"package-test","package":"nanolinter-ci","name":"imageplan_contract","gate":"policy","ignored":true}]}`

	for _, result := range []string{"pass", "ignored", "empty", "failure", "truncated", "malformed"} {
		t.Run(result, func(t *testing.T) {
			tool := selectionTool(func(_ context.Context, in appbuild.GoRunInput) error {
				want := []string{"test", "--locked", "--package", "nanolinter-ci", "--test", "imageplan_contract", "--", "--ignored"}
				if !reflect.DeepEqual(in.Args, want) {
					t.Fatalf("contract argv %v, want %v", in.Args, want)
				}

				switch result {
				case "pass":
					_, _ = io.WriteString(in.Stdout, selectionCargoPass)
				case "ignored":
					_, _ = io.WriteString(in.Stdout, "running 3 tests\ntest result: ok. 0 passed; 0 failed; 3 ignored; 0 measured; 0 filtered out; finished in 0.00s\n")
				case "failure":
					return writeSelectionFailure(in)
				case "truncated":
					_, _ = io.WriteString(in.Stdout, "running 3 tests\n")
				case "malformed":
					_, _ = io.WriteString(in.Stdout, "test result: invalid\n")
				}

				return nil
			})

			var receipt bytes.Buffer

			err := appbuild.CargoTest(t.Context(), tool, &receipt, io.Discard, appbuild.CargoTestInput{SelectionJSON: selection})
			if (err == nil) != (result == "pass") || strings.Contains(receipt.String(), "result=findings") || !strings.Contains(receipt.String(), "enforcement=true") {
				t.Fatalf("result=%s err=%v receipt=%s", result, err, &receipt)
			}
		})
	}
}
