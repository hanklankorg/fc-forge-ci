// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package safeexec_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
func TestCommand_CancellationKillsTheProcessGroup(t *testing.T) {
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

	if running, err := processExecuting(grandchild); err != nil || !running {
		t.Fatalf("grandchild was not running before cancellation: %v", err)
	}

	cancel()

	_ = cmd.Wait()

	deadline := time.Now().Add(5 * time.Second)

	for {
		running, err := processExecuting(grandchild)
		if err != nil {
			t.Fatal(err)
		}

		if !running {
			break
		}

		if time.Now().After(deadline) {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)

			t.Fatalf("grandchild %d outlived the cancelled command", grandchild)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// kill(pid, 0) still succeeds for a zombie until the container's PID 1 reaps
// it. A zombie cannot execute; cancellation must stop execution, not depend on
// an unrelated init's reaping schedule. A process reaped between the open of
// its stat file and the read is gone as well: the read then fails with ESRCH,
// not with a missing file. Unknown proc state remains an error.
func processExecuting(pid int) (bool, error) {
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	_, rest, found := strings.Cut(string(body), ") ")
	fields := strings.Fields(rest)

	if !found || len(fields) == 0 {
		return false, fmt.Errorf("invalid process state for %d", pid)
	}

	return fields[0] != "Z" && fields[0] != "X", nil
}

func TestProcessExecutingRecognizesLiveProcess(t *testing.T) {
	if running, err := processExecuting(os.Getpid()); err != nil || !running {
		t.Fatalf("current process must still execute: running=%t err=%v", running, err)
	}
}

// On a terminal the command stays in the foreground process group, where
// Ctrl-C reaches it and it may read the terminal.
func TestCommand_StaysInTheForegroundGroupOnATerminal(t *testing.T) {
	safeexec.UseInteractive(t, true)

	cmd := safeexec.Command(t.Context(), "/bin/true")
	if cmd.SysProcAttr != nil {
		t.Fatalf("an interactive command left its process group: %+v", cmd.SysProcAttr)
	}
}
