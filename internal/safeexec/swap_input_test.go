// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package safeexec_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

func TestSwapEnabled_RejectsOversizedInput(t *testing.T) {
	path := writeSwapsFile(t, "Filename\tType\tSize\tUsed\tPriority\n")
	// A sparse file exercises the documented 64-MiB input limit without
	// producing a large fixture on disk. No unbounded source is read.
	if err := os.Truncate(path, 64<<20+1); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FORGE_CI_PROC_SWAPS", path)

	on, err := safeexec.SwapEnabled()
	if on || !errors.Is(err, errs.ErrMalformedInput) {
		t.Fatalf("oversized swap input: on=%t err=%v; want classified refusal", on, err)
	}

	state, err := safeexec.RequireNoSwap(false)
	if err != nil || state != safeexec.SwapUndetermined {
		t.Fatalf("unreadable swap input: state=%s err=%v; want undetermined", state, err)
	}
}

func TestSwapEnabled_RefusesFIFOWithoutAWriter(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "swaps")

	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	// Only this owned child can block on the FIFO; CommandContext kills and
	// Wait reaps it on timeout. No writer, host proc file or other tool is used.
	cmd := exec.CommandContext(ctx, binary, "-test.v", "-test.run=^TestSwapInputFIFOChild$") //nolint:gosec // owned test executable and exact helper selection.
	cmd.Dir = root
	cmd.Env = []string{"HOME=" + root, "TMPDIR=" + root, "FORGE_CI_SWAP_INPUT_CHILD=1", "FORGE_CI_PROC_SWAPS=" + path}
	cmd.WaitDelay = time.Second

	output, err := cmd.CombinedOutput()

	if ctx.Err() != nil {
		t.Fatalf("swap reader blocked on a FIFO without a writer: %v", ctx.Err())
	}

	if err != nil || !strings.Contains(string(output), "--- PASS: TestSwapInputFIFOChild") {
		t.Fatalf("FIFO refusal child: %v\n%s", err, output)
	}
}

func TestSwapInputFIFOChild(t *testing.T) {
	if os.Getenv("FORGE_CI_SWAP_INPUT_CHILD") != "1" {
		return
	}

	on, err := safeexec.SwapEnabled()
	if on || !errors.Is(err, errs.ErrMissingInput) {
		t.Fatalf("FIFO input: on=%t err=%v; want regular-file refusal", on, err)
	}

	state, err := safeexec.RequireNoSwap(false)
	if err != nil || state != safeexec.SwapUndetermined {
		t.Fatalf("FIFO policy: state=%s err=%v; want undetermined", state, err)
	}
}

func TestSwapEnabled_PreservesLiteralPathsAndRegularAliases(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	const active = "Filename\tType\tSize\tUsed\tPriority\n/dev/fixture\tpartition\t1\t0\t-2\n"

	if err := os.WriteFile("-", []byte(active), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join(root, "-"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}

	stdin, err := os.Open(writeSwapsFile(t, "Filename\tType\tSize\tUsed\tPriority\n"))
	if err != nil {
		t.Fatal(err)
	}

	previous := os.Stdin
	os.Stdin = stdin //nolint:reassign // serial test; an owned stdin canary detects accidental '-' sentinel handling.

	t.Cleanup(func() {
		os.Stdin = previous //nolint:reassign // restore the test process's stdin before closing the canary.
		_ = stdin.Close()
	})

	for _, path := range []string{"-", "alias"} {
		t.Setenv("FORGE_CI_PROC_SWAPS", path)

		on, readErr := safeexec.SwapEnabled()
		if readErr != nil || !on {
			t.Fatalf("literal regular path %q: on=%t err=%v", path, on, readErr)
		}
	}

	position, err := stdin.Seek(0, io.SeekCurrent)
	if err != nil || position != 0 {
		t.Fatalf("swap input consumed stdin: offset=%d err=%v", position, err)
	}
}
