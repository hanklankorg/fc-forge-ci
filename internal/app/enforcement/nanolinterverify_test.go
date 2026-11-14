// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

type verifyRunner struct {
	delegate runner
	args     []string
}

func (r *verifyRunner) Run(ctx context.Context, out, stderr io.Writer, args []string) (int, error) {
	r.args = slices.Clone(args)

	return r.delegate.Run(ctx, out, stderr, args)
}

func TestNanolinterVerifyOwnsTheStandardPRArguments(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input NanolinterVerifyInput
		args  []string
	}{
		{"floor", NanolinterVerifyInput{RequiredLints: "secrets"}, []string{"nanolinter", "verify", "--require", "secrets", "--report", "sarif", "--out", "-"}},
		{"offline exclusions", NanolinterVerifyInput{RequiredLints: "secrets", DisableChecks: "osv,trivy-fs", Offline: true}, []string{"nanolinter", "verify", "--require", "secrets", "--disable-check", "osv,trivy-fs", "--offline", "--report", "sarif", "--out", "-"}},
		{"explicit no floor", NanolinterVerifyInput{}, []string{"nanolinter", "verify", "--report", "sarif", "--out", "-"}},
		{"opaque values", NanolinterVerifyInput{RequiredLints: "one value", DisableChecks: "other value"}, []string{"nanolinter", "verify", "--require", "one value", "--disable-check", "other value", "--report", "sarif", "--out", "-"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tool := &verifyRunner{delegate: runner{body: `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"nanolinter"}},"results":[]}]}`}}
			if err := NanolinterVerify(t.Context(), tool, io.Discard, io.Discard, test.input); err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(tool.args, test.args) {
				t.Fatalf("args = %q, want %q", tool.args, test.args)
			}
		})
	}
}

func TestNanolinterVerifyKeepsExistingPolicyAndExecutionFailureSemantics(t *testing.T) {
	t.Parallel()

	for _, enforced := range []string{"true", "false"} {
		ctx := WithAdmin(t.Context(), enforcement.Admin{enforcement.Lint: enforced})
		tool := &verifyRunner{delegate: runner{status: 1, body: `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"nanolinter"}},"results":[{"ruleId":"format","properties":{"source":"curated","check_id":"format","class":"formatter","status":"fail"}}]}]}`}}

		err := NanolinterVerify(ctx, tool, io.Discard, io.Discard, NanolinterVerifyInput{})
		if errors.Is(err, errs.ErrValidation) != (enforced == "true") {
			t.Fatalf("enforced=%s: findings error = %v", enforced, err)
		}

		tool = &verifyRunner{delegate: runner{err: io.ErrUnexpectedEOF}}
		if err := NanolinterVerify(ctx, tool, io.Discard, io.Discard, NanolinterVerifyInput{}); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("enforced=%s: execution failure was lost: %v", enforced, err)
		}
	}
}
