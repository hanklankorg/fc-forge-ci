// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// LintRunner returns the linter's exit status separately from execution errors.
type LintRunner interface {
	Run(ctx context.Context, out, stderr io.Writer, args []string) (int, error)
}

// Nanolinter runs before applying policy. Both process status and per-check
// metadata must describe findings; operational failures remain errors.
func Nanolinter(ctx context.Context, runner LintRunner, out, stderr io.Writer, args []string, sarifFile string) error {
	if len(args) == 0 {
		return fmt.Errorf("a nanolinter command is required: %w", errs.ErrUsage)
	}

	policy, err := Resolve(ctx)
	if err != nil {
		return err
	}

	return nanolinter(ctx, policy, runner, out, stderr, args, sarifFile)
}

func nanolinter(ctx context.Context, policy enforcement.Policy, runner LintRunner, out, stderr io.Writer, args []string, sarifFile string) error {
	if err := retireSARIF(sarifFile); err != nil {
		return err
	}

	capture := &boundedCapture{}

	status, err := runner.Run(ctx, io.MultiWriter(out, capture), stderr, args)
	if ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}

	if err != nil {
		return err
	}

	if status != 0 && status != 1 {
		return fmt.Errorf("lint command could not complete (exit %d): %w", status, errs.ErrPrerequisite)
	}

	if capture.overflow {
		return fmt.Errorf("nanolinter report exceeds the bounded report size: %w", errs.ErrMalformedInput)
	}

	report, err := enforcement.ParseNanolinter(capture.body.Bytes())
	if err != nil {
		return err
	}

	if sarifFile != "" {
		if err := cliio.WriteFile(sarifFile, append(report.SecuritySARIF, '\n'), 0o644); err != nil {
			return fmt.Errorf("write security SARIF: %w", err)
		}
	}

	if status == 1 && report.LintFindings+report.VulnerabilityFindings == 0 && report.Unavailable == nil {
		return fmt.Errorf("nanolinter failed without classified findings: %w", errs.ErrValidation)
	}

	gate := func(check enforcement.Check, count int) error {
		decision, err := policy.Decide(check, count)
		if err != nil {
			return err
		}

		outcome, detail := report.Outcome(check)

		return reportOutcome(out, decision, outcome, detail)
	}

	// Findings are reported and gated first; a check without a verdict then
	// fails the command whatever the policy says about findings.
	return errors.Join(gate(enforcement.Lint, report.LintFindings), gate(enforcement.Vulnerabilities, report.VulnerabilityFindings), report.Unavailable)
}

// boundedCapture keeps one structured report, refusing to grow past 8 MiB.
// It receives only the linter's stdout, so it has a single writer.
type boundedCapture struct {
	body     bytes.Buffer
	overflow bool
}

func (capture *boundedCapture) Write(body []byte) (int, error) {
	const limit = 8 << 20
	if capture.body.Len()+len(body) > limit {
		capture.overflow = true

		return len(body), nil
	}

	_, _ = capture.body.Write(body)

	return len(body), nil
}

func retireSARIF(path string) error {
	if path == "" {
		return nil
	}

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("SARIF destination must be a regular file: %w", errs.ErrUsage)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("retire previous SARIF: %w", err)
	}

	return nil
}

// Show prints the effective policy of this invocation for administrators and
// run receipts: each setting, where it came from, and the SHA-256 of the
// embedded default it started from.
func Show(ctx context.Context, out io.Writer) error {
	policy, err := Resolve(ctx)
	if err != nil {
		return err
	}

	sources := make(map[string]enforcement.Source, len(enforcement.Checks))
	for _, check := range enforcement.Checks {
		sources[enforcement.Setting(check)] = policy.Source(check)
	}

	return json.NewEncoder(out).Encode(struct {
		Policy  enforcement.Policy            `json:"settings"`
		Sources map[string]enforcement.Source `json:"sources"`
		SHA256  string                        `json:"default-sha256"`
	}{policy, sources, enforcement.Digest()})
}

// Tests supports native test commands in custom provider workflows. The workflow
// owns the fixed command/protocol; neither is an administrator-policy override.
func Tests(ctx context.Context, runner LintRunner, out, stderr io.Writer, protocol enforcement.TestProtocol, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("a test command is required: %w", errs.ErrUsage)
	}

	switch protocol {
	case enforcement.GoJSON, enforcement.Maven, enforcement.Cargo, enforcement.Node, enforcement.Gradle, enforcement.XCTest:
	default:
		return fmt.Errorf("unknown native test protocol %q: %w", protocol, errs.ErrUsage)
	}

	_, err := TestRun(ctx, out, stderr, protocol, func(stdout, stderr io.Writer) error {
		status, err := runner.Run(ctx, stdout, stderr, args)
		if err != nil {
			return err
		}

		if status == 0 {
			return nil
		}

		if status == 1 || status == 101 || status == 65 {
			return fmt.Errorf("%w: %w", commandExitError(status), errs.ErrValidation)
		}

		return fmt.Errorf("%w: %w", commandExitError(status), errs.ErrPrerequisite)
	})

	return err
}

type commandExitError int

func (status commandExitError) Error() string {
	return fmt.Sprintf("application test command exited %d", status)
}
func (status commandExitError) ExitCode() int { return int(status) }
