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

// TestRun always executes tests. Only a recognized fresh assertion-failure
// summary, an ordinary tool failure, and non-enforcing admin policy allow a
// continuation. An absent/truncated/malformed summary never grants a waiver.
func TestRun(ctx context.Context, out, stderr io.Writer, protocol enforcement.TestProtocol, run func(io.Writer, io.Writer) error) (bool, error) {
	policy, err := Resolve(ctx)
	if err != nil {
		return false, err
	}

	return testRun(ctx, policy, out, stderr, protocol, run)
}

func testRun(ctx context.Context, policy enforcement.Policy, out, stderr io.Writer, protocol enforcement.TestProtocol, run func(io.Writer, io.Writer) error) (bool, error) {
	if policy.EnforceApplicationTests {
		err := run(out, stderr)
		if ctx.Err() != nil {
			return false, errors.Join(err, ctx.Err())
		}

		return false, err
	}

	output := enforcement.NewTestOutput(protocol)

	err := run(io.MultiWriter(out, output.Stdout()), io.MultiWriter(stderr, output.Stderr()))
	if ctx.Err() != nil {
		return false, errors.Join(err, ctx.Err())
	}

	if err == nil {
		return false, FindingsFor(out, policy, enforcement.ApplicationTests, 0)
	}

	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) {
		notWaivable(out, policy, "the test command failed without a test exit status")

		return false, err
	}

	if output.Overlong() {
		notWaivable(out, policy, fmt.Sprintf("a test output line exceeded %d MiB, so no summary could be trusted", enforcement.MaxTestLineBytes>>20))

		return false, err
	}

	knownFailure := exit.ExitCode() == 1 || (protocol == enforcement.Cargo && exit.ExitCode() == 101) || (protocol == enforcement.XCTest && exit.ExitCode() == 65)
	if !knownFailure {
		notWaivable(out, policy, fmt.Sprintf("exit status %d is not the test tool's assertion-failure status", exit.ExitCode()))

		return false, err
	}

	count := output.FailedTests()
	if count == 0 {
		notWaivable(out, policy, "no complete assertion-failure summary: test errors, setup and compile failures are never waived")

		return false, err
	}

	if reportErr := FindingsFor(out, policy, enforcement.ApplicationTests, count); reportErr != nil {
		return false, errors.Join(err, reportErr)
	}

	return true, nil
}

// RequireTests rejects the old consumer opt-out independently of enforcement:
// report-only still means running tests, never skipping them.
func RequireTests(ctx context.Context, skip bool) error {
	if skip {
		return fmt.Errorf("skip-tests cannot disable administrator-required test execution: %w", errs.ErrUsage)
	}

	_, err := Resolve(ctx)

	return err
}

// notWaivable says why report-only did not continue past a failed test run,
// so a log never shows a blocked build under a non-enforcing policy without
// the reason. It is best effort: the test error stays the result.
func notWaivable(out io.Writer, policy enforcement.Policy, reason string) {
	decision, err := policy.Decide(enforcement.ApplicationTests, 0)
	if err != nil {
		return
	}

	_, _ = fmt.Fprintf(out, "Enforcement policy default-sha256:%s: %s result=not-waivable enforcement=%t source=%s reason=%q\n",
		decision.PolicySHA256, decision.Check, decision.Enforced, decision.Source, reason)
}
