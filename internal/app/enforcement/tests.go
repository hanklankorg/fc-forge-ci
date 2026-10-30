// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// TestRun always executes tests. Only a recognized fresh assertion-failure
// summary, an ordinary tool failure, and non-enforcing admin policy allow a
// continuation. An absent/truncated/malformed summary never grants a waiver.
func TestRun(ctx context.Context, out, stderr io.Writer, protocol enforcement.TestProtocol, run func(io.Writer, io.Writer) error) (bool, error) {
	policy, err := enforcement.Current()
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
	capture := &testCapture{}
	err := run(io.MultiWriter(out, capture), io.MultiWriter(stderr, capture))
	if ctx.Err() != nil {
		return false, errors.Join(err, ctx.Err())
	}
	if err == nil {
		return false, findingsFor(out, policy, enforcement.ApplicationTests, 0)
	}
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || (exit.ExitCode() != 1 && !(protocol == enforcement.Cargo && exit.ExitCode() == 101) && !(protocol == enforcement.XCTest && exit.ExitCode() == 65)) || capture.overflow {
		return false, err
	}
	count := enforcement.FailedTests(protocol, capture.body.String())
	if count == 0 {
		return false, err
	}
	if reportErr := findingsFor(out, policy, enforcement.ApplicationTests, count); reportErr != nil {
		return false, errors.Join(err, reportErr)
	}
	return true, nil
}

// RequireTests rejects the old consumer opt-out independently of enforcement:
// report-only still means running tests, never skipping them.
func RequireTests(skip bool) error {
	if skip {
		return fmt.Errorf("skip-tests cannot disable administrator-required test execution: %w", errs.ErrUsage)
	}
	_, err := enforcement.Current()
	return err
}

type testCapture struct {
	mu       sync.Mutex
	body     bytes.Buffer
	overflow bool
}

func (capture *testCapture) Write(body []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	const limit = 8 << 20
	if capture.body.Len()+len(body) > limit {
		capture.overflow = true
		return len(body), nil
	}
	_, _ = capture.body.Write(body)
	return len(body), nil
}
