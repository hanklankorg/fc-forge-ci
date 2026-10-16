// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/diggsweden/forge-ci/internal/retry"
)

func TestInterruptedCleanupHasOneSharedGracePeriod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		work, interrupt := context.WithCancel(t.Context())

		cleanup, closeCleanup := interruptedCleanup(work)
		defer closeCleanup()

		interrupt()
		synctest.Wait()

		if cleanup.Err() != nil {
			t.Fatal("interruption canceled cleanup before it could run")
		}

		time.Sleep(timeoutScratch - time.Second)

		last, cancel := context.WithTimeout(cleanup, timeoutScratch)
		defer cancel()

		time.Sleep(time.Second)
		synctest.Wait()

		if !errors.Is(last.Err(), context.Canceled) {
			t.Fatal("a later cleanup escaped the shared interruption budget")
		}
	})
}

func TestArchiveReadsStopOnCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	reader := &contextReadCloser{ReadCloser: io.NopCloser(strings.NewReader("archive")), ctx: ctx}

	cancel()

	if count, err := reader.Read(make([]byte, 1)); count != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("archive hashing continued after cancellation: n=%d err=%v", count, err)
	}
}

func TestSuiteSignalsRunNormalTestCleanup(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			root := t.TempDir()

			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()

			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSuiteSignalFixture$") //nolint:gosec // The current test executable, running an inert signal fixture only.
			command.Env = []string{"FORGE_CI_LIVE_SIGNAL_FIXTURE=" + root}

			var output bytes.Buffer

			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() { done <- command.Wait() }()

			waitFixtureFile(t, ctx, root, "ready", done)

			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}

			err := <-done

			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 130 {
				t.Fatalf("interrupted suite status: %v\n%s", err, output.String())
			}

			if body, err := os.ReadFile(filepath.Join(root, "cleaned")); err != nil || string(body) != "cleaned" {
				t.Fatalf("interruption skipped ordinary cleanup: %v", err)
			}
		})
	}
}

// Only the subprocess above enables this fixture. It reaches no forge and
// records cleanup through testing.T itself, not a second cleanup registry.
func TestSuiteSignalFixture(t *testing.T) {
	root := os.Getenv("FORGE_CI_LIVE_SIGNAL_FIXTURE")
	if root == "" {
		return
	}

	files, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = files.Close() })

	ctx := Context(t)
	t.Cleanup(func() {
		cleanup, cancel := cleanupContext(5 * time.Second)
		defer cancel()

		if err := cleanup.Err(); err != nil {
			t.Errorf("cleanup inherited interruption: %v", err)

			return
		}

		if os.Getenv("FORGE_CI_LIVE_CLEANUP_HANDSHAKE") == "1" {
			if err := files.WriteFile("cleaning", nil, 0o600); err != nil {
				t.Error(err)

				return
			}

			if err := retry.Run(cleanup, nil, 500, 10*time.Millisecond, func() error {
				_, err := files.Stat("finish-cleanup")

				return err
			}, retry.WithBackoff(retry.Constant)); err != nil {
				t.Errorf("cleanup handshake failed: %v", err)

				return
			}
		}

		if err := files.WriteFile("cleaned", []byte("cleaned"), 0o600); err != nil {
			t.Error(err)
		}
	})

	if err := files.WriteFile("ready", nil, 0o600); err != nil {
		t.Fatal(err)
	}

	<-ctx.Done()
	t.Error("fixture interrupted")
}

func waitFixtureFile(t *testing.T, ctx context.Context, root, name string, done <-chan error) {
	t.Helper()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return
		}

		select {
		case err := <-done:
			t.Fatalf("signal fixture exited before readiness: %v", err)
		case <-ctx.Done():
			t.Fatal("signal fixture did not become ready")
		case <-ticker.C:
		}
	}
}
