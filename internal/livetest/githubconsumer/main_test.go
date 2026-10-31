// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package githubconsumer_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/retry"
)

func TestMain(m *testing.M) {
	os.Exit(livetest.RunSuite(m.Run))
}

// This exercises the hosted package's normal TestMain without loading a hosted
// context or reaching a provider. Neither this test nor its fixture is a scenario.
func TestHostedSuiteSignalsRunCleanup(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHostedSuiteLifecycleFixture$")
			command.Env = []string{"FORGE_CI_LIVE_HOSTED_SIGNAL_FIXTURE=" + root}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = command.Process.Kill() }()
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			if err := retry.Run(ctx, nil, 1000, 10*time.Millisecond, func() error {
				_, err := os.Stat(filepath.Join(root, "ready"))
				return err
			}, retry.WithBackoff(retry.Constant)); err != nil {
				t.Fatalf("hosted lifecycle fixture not ready: %v", err)
			}
			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			var exit *exec.ExitError
			if err := <-done; !errors.As(err, &exit) || exit.ExitCode() != 130 {
				t.Fatalf("hosted suite interruption status: %v", err)
			}
			if body, err := os.ReadFile(filepath.Join(root, "cleaned")); err != nil || string(body) != "cleaned" {
				t.Fatalf("hosted suite skipped cleanup: %v", err)
			}
		})
	}
}

func TestHostedSuiteLifecycleFixture(t *testing.T) {
	root := os.Getenv("FORGE_CI_LIVE_HOSTED_SIGNAL_FIXTURE")
	if root == "" {
		return
	}
	ctx := livetest.Context(t)
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(root, "cleaned"), []byte("cleaned"), 0o600); err != nil {
			t.Error(err)
		}
	})
	if err := os.WriteFile(filepath.Join(root, "ready"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	t.Error("fixture interrupted")
}
