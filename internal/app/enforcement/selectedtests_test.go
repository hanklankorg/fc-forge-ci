// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const cargoPassed = "running 1 test\ntest safe ... ok\ntest result: ok. 1 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.00s\n"
const cargoFailed = "running 1 test\ntest safe ... FAILED\ntest result: FAILED. 0 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.00s\n"
const goPassed = `{"Action":"start","Package":"safe"}
{"Action":"run","Package":"safe","Test":"TestSafe"}
{"Action":"pass","Package":"safe","Test":"TestSafe"}
{"Action":"pass","Package":"safe"}
`
const goFailed = `{"Action":"start","Package":"safe"}
{"Action":"run","Package":"safe","Test":"TestSafe"}
{"Action":"fail","Package":"safe","Test":"TestSafe"}
{"Action":"fail","Package":"safe"}
`

func TestMalformedSelectedReportKeepsErrorTextButNotExitCode(t *testing.T) {
	for _, tc := range []struct {
		name, commandText string
		commandError      error
	}{
		{"exit zero", "<nil>", nil},
		{"native failure", "native test result", testExitError(101)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := selectedNativeRun(enforcement.Cargo, func(io.Writer, io.Writer) error {
				return tc.commandError
			})(io.Discard, io.Discard)

			want := "selected tests require a complete nonempty native report (command error: " + tc.commandText + "): " + errs.ErrValidation.Error()
			if err == nil || err.Error() != want || !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("malformed-report error: got %v, want %q", err, want)
			}

			var exitCode interface{ ExitCode() int }
			if errors.As(err, &exitCode) {
				t.Fatal("malformed report exposed a waivable native ExitCode")
			}
		})
	}
}

func TestSelectedTestsRequireFreshNonemptyCompleteReportsUnderBothPolicies(t *testing.T) {
	for _, enforce := range []string{"true", "false"} {
		for _, tc := range []struct {
			name        string
			protocol    enforcement.TestProtocol
			out, stderr string
			err         error
			allowed     bool
		}{
			{"cargo pass", enforcement.Cargo, cargoPassed, "", nil, true},
			{"cargo multiple executables refused", enforcement.Cargo, cargoPassed + cargoPassed, "", nil, false},
			{"cargo later unrun harness", enforcement.Cargo, cargoFailed + "running 1 test\n", "error: test failed, to rerun pass `--test safe`\n", testExitError(101), false},
			{"cargo mismatched running total", enforcement.Cargo, strings.Replace(cargoPassed, "running 1 test", "running 2 tests", 1), "", nil, false},
			{"go pass", enforcement.GoJSON, goPassed, "", nil, true},
			{"cargo assertion", enforcement.Cargo, cargoFailed, "error: test failed, to rerun pass `--bin safe`\n", testExitError(101), enforce == "false"},
			{"go assertion", enforcement.GoJSON, goFailed, "", testExitError(1), enforce == "false"},
			{"missing exit zero", enforcement.Cargo, "", "", nil, false},
			{"go missing exit zero", enforcement.GoJSON, "", "", nil, false},
			{"cargo zero", enforcement.Cargo, "running 0 tests\ntest result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.00s\n", "", nil, false},
			{"cargo ignored", enforcement.Cargo, "running 1 test\ntest result: ok. 0 passed; 0 failed; 1 ignored; 0 measured; 0 filtered out; finished in 0.00s\n", "", nil, false},
			{"go no matching tests", enforcement.GoJSON, "{\"Action\":\"start\",\"Package\":\"safe\"}\n{\"Action\":\"pass\",\"Package\":\"safe\"}\n", "", nil, false},
			{"go truncated", enforcement.GoJSON, strings.TrimSuffix(goPassed, "{\"Action\":\"pass\",\"Package\":\"safe\"}\n"), "", nil, false},
			{"cargo truncated", enforcement.Cargo, "running 1 test\ntest safe ... ok\n", "", nil, false},
			{"cargo missing failure terminal", enforcement.Cargo, cargoFailed, "", testExitError(101), false},
			{"cargo infrastructure after terminal", enforcement.Cargo, cargoFailed, "error: test failed, to rerun pass `--test safe`\ndisk full\n", testExitError(101), false},
			{"go malformed", enforcement.GoJSON, goPassed + "not json\n", "", nil, false},
			{"cargo setup after assertion", enforcement.Cargo, cargoFailed, "error: could not compile package\n", testExitError(101), false},
			{"cargo signal after assertion", enforcement.Cargo, cargoFailed, "error: test failed, to rerun pass `--test safe`\nprocess didn't exit successfully (signal: 9)\n", testExitError(101), false},
			{"go infrastructure after assertion", enforcement.GoJSON, goFailed, "disk full\n", testExitError(1), false},
			{"unexpected exit", enforcement.Cargo, cargoFailed, "", testExitError(125), false},
			{"startup failure", enforcement.Cargo, cargoFailed, "", errors.New("cannot start"), false},
			{"failed report on exit zero", enforcement.Cargo, cargoFailed, "", nil, false},
			{"overlong", enforcement.Cargo, cargoPassed + strings.Repeat("x", enforcement.MaxTestLineBytes+1), "", nil, false},
		} {
			t.Run(enforce+"/"+tc.name, func(t *testing.T) {
				ctx := WithAdmin(t.Context(), enforcement.Admin{enforcement.ApplicationTests: enforce})

				var receipt bytes.Buffer

				err := SelectedTestRun(ctx, &receipt, io.Discard, tc.protocol, func(out, stderr io.Writer) error {
					_, _ = io.WriteString(out, tc.out)
					_, _ = io.WriteString(stderr, tc.stderr)

					return tc.err
				})
				if (err == nil) != tc.allowed {
					t.Fatalf("allowed=%t, err=%v, receipt=%s", tc.allowed, err, &receipt)
				}

				if tc.allowed && tc.err != nil && !strings.Contains(receipt.String(), "result=findings") {
					t.Fatal("assertion findings hidden")
				}
			})
		}
	}
}

func TestSelectedTestsCancellationIsNeverWaived(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	err := SelectedTestRun(ctx, io.Discard, io.Discard, enforcement.Cargo, func(out, _ io.Writer) error {
		_, _ = io.WriteString(out, cargoFailed)

		cancel()

		return testExitError(101)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestSelectedGoPolicyAlwaysBlocksFailuresAndRequiresEvidence(t *testing.T) {
	for _, enforce := range []string{"true", "false"} {
		for _, tc := range []struct {
			name, report string
			err          error
			allowed      bool
		}{
			{"pass", goPassed, nil, true},
			{"assertion", goFailed, testExitError(1), false},
			{"missing", "", nil, false},
			{"truncated", strings.TrimSuffix(goPassed, "{\"Action\":\"pass\",\"Package\":\"safe\"}\n"), nil, false},
			{"zero", "{\"Action\":\"start\",\"Package\":\"safe\"}\n{\"Action\":\"pass\",\"Package\":\"safe\"}\n", nil, false},
		} {
			t.Run(enforce+"/"+tc.name, func(t *testing.T) {
				ctx := WithAdmin(t.Context(), enforcement.Admin{enforcement.ApplicationTests: enforce})

				var receipt bytes.Buffer

				err := SelectedPolicyTestRun(ctx, &receipt, io.Discard, enforcement.GoJSON, func(out, _ io.Writer) error {
					_, _ = io.WriteString(out, tc.report)

					return tc.err
				})
				if (err == nil) != tc.allowed || strings.Contains(receipt.String(), "result=findings") || !strings.Contains(receipt.String(), "enforcement=true") {
					t.Fatalf("allowed=%t err=%v receipt=%s", tc.allowed, err, &receipt)
				}
			})
		}
	}
}
