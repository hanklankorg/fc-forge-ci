// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package policycommand executes the check selected by a trusted workflow. It
// never interprets shell text or takes policy from the child environment.
package policycommand

import (
	"context"
	"errors"
	"io"
	"os/exec"

	"github.com/diggsweden/forge-ci/internal/safeexec"
)

type Runner struct{}

func (Runner) Run(ctx context.Context, out, stderr io.Writer, args []string) (int, error) {
	command := safeexec.Command(ctx, args[0], args[1:]...)
	command.Stdout, command.Stderr = out, stderr
	err := command.Run()
	if err == nil {
		return 0, nil
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode(), nil
	}
	return 0, safeexec.WrapError(err, args[0], "lint")
}
