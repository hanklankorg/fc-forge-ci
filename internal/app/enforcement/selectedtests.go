// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// SelectedTestRun extends the existing policy gate with mandatory fresh native
// evidence for an explicitly selected invocation. Invalid evidence deliberately
// does not expose the original ExitCode: it must never qualify for a waiver.
func SelectedTestRun(ctx context.Context, out, stderr io.Writer, protocol enforcement.TestProtocol, run func(io.Writer, io.Writer) error) error {
	_, err := TestRun(ctx, out, stderr, protocol, selectedNativeRun(protocol, run))
	return err
}

// SelectedPolicyTestRun is independently blocking. Application-test enforcement
// (including its report-only default) never grants a policy-test waiver.
func SelectedPolicyTestRun(ctx context.Context, out, stderr io.Writer, protocol enforcement.TestProtocol, run func(io.Writer, io.Writer) error) error {
	if _, err := Resolve(ctx); err != nil {
		return err
	}
	err := selectedNativeRun(protocol, run)(out, stderr)
	if ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}
	result := "pass"
	if err != nil {
		result = "failure"
	}
	_, receiptErr := fmt.Fprintf(out, "Selected policy tests: result=%s enforcement=true\n", result)
	return errors.Join(err, receiptErr)
}

func selectedNativeRun(protocol enforcement.TestProtocol, run func(io.Writer, io.Writer) error) func(io.Writer, io.Writer) error {
	return func(stdout, stderr io.Writer) error {
		report := enforcement.NewSelectedReport(protocol)
		err := run(io.MultiWriter(stdout, report.Output().Stdout()), io.MultiWriter(stderr, report.Output().Stderr()))
		if !report.Valid(err == nil) {
			return fmt.Errorf("selected tests require a complete nonempty native report (command error: %v): %w", err, errs.ErrValidation)
		}
		return err
	}
}
