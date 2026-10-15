// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// RunSuite installs these before m.Run starts any tests. Ordinary unit tests
// use their own testing context and the existing per-operation cleanup bounds.
var (
	suiteContext = context.Background() //nolint:gochecknoglobals // Set once by the live TestMain before tests start.
	cleanupRoot  = context.Background() //nolint:gochecknoglobals // Set once by the live TestMain before tests start.
)

// RunSuite lets an interrupted live test unwind through its normal cleanup.
// Cleanup gets a separate, shared two-minute grace period, not the canceled
// work context. The launcher runs the compiled suite directly so go test cannot
// terminate it before those callbacks finish. Interrupted runs return 130.
func RunSuite(run func() int) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cleanup, closeCleanup := interruptedCleanup(ctx)
	defer closeCleanup()

	suiteContext, cleanupRoot = ctx, cleanup //nolint:fatcontext // TestMain installs the process lifetime once, before any test goroutine starts.

	status := run()

	if ctx.Err() != nil {
		return 130
	}

	return status
}

func interruptedCleanup(work context.Context) (context.Context, func()) {
	cleanup, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(work, func() { time.AfterFunc(timeoutScratch, cancel) })

	return cleanup, func() { stop(); cancel() }
}

// Context ends on either test completion or suite interruption. It registers
// only context disposal; the test's existing resource cleanup keeps its order.
func Context(tb TB) context.Context {
	tb.Helper()

	if suiteContext.Done() == nil {
		return tb.Context()
	}

	ctx, cancel := context.WithCancel(tb.Context())
	stop := context.AfterFunc(suiteContext, cancel)

	if suiteContext.Err() != nil {
		cancel()
	}

	tb.Cleanup(func() { stop(); cancel() })

	return ctx
}

func cleanupContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cleanupRoot, timeout)
}
