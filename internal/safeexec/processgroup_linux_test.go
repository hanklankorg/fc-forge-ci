// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package safeexec_test

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// A cancelled command took only its direct child with it: a grandchild, such
// as a build step's own process, kept running after a timeout or a runner's
// cancellation. Off a terminal the whole process group goes.
func TestCommand_CancellationKillsTheProcessGroup(t *testing.T) { //nolint:paralleltest // replaces the package's terminal check.
	safeexec.UseInteractive(t, false)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cmd := safeexec.Command(ctx, "/bin/sh", "-c", "sleep 60 & echo $!; wait")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}

	grandchild, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}

	cancel()

	_ = cmd.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(grandchild, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)

			t.Fatalf("grandchild %d outlived the cancelled command", grandchild)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// On a terminal the command stays in the foreground process group, where
// Ctrl-C reaches it and it may read the terminal.
func TestCommand_StaysInTheForegroundGroupOnATerminal(t *testing.T) { //nolint:paralleltest // replaces the package's terminal check.
	safeexec.UseInteractive(t, true)

	cmd := safeexec.Command(t.Context(), "/bin/true")
	if cmd.SysProcAttr != nil {
		t.Fatalf("an interactive command left its process group: %+v", cmd.SysProcAttr)
	}
}
