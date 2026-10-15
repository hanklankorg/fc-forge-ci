// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package safeexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/term"
)

// interactive reports whether this process reads a terminal. Tests replace it.
var interactive = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) } //nolint:gochecknoglobals // test seam for the terminal check.

// configureProcessGroup makes a cancelled command take its whole process
// tree with it. exec's default cancellation kills the direct child only, so
// a build step's own children (buildah's, a git helper) outlived a run that
// a timeout or a runner cancelled, when the signal came from no terminal.
// The child gets its own process group and cancellation kills the group.
//
// On a terminal the command stays in the foreground group: Ctrl-C already
// reaches every process there, and a child moved out of it would be stopped
// the moment it read the terminal, as an SSH passphrase prompt does.
func configureProcessGroup(cmd *exec.Cmd) {
	if interactive() {
		return
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}

		return err
	}
}
