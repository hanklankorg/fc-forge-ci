// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package trivy wraps the system `trivy` binary. The runtime image
// bakes trivy in; local runs need it on PATH.
package trivy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"

	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// Adapter wraps the trivy binary. Bin is overridable for tests.
type Adapter struct {
	Bin string // empty → "trivy"
}

// New returns an Adapter using the system trivy.
func New() *Adapter { return &Adapter{} }

// RunInherit invokes `trivy` with args, streaming stdout/stderr, and returns
// the process exit code and any error from starting or waiting.
//
// Trivy runs with only safeexec.GateToolEnv from the environment, since
// TRIVY_* variables would override the engine's flags, with configuration
// file loading disabled, in a private empty working directory, so nothing in
// the caller's directory is discovered. Path arguments must therefore be
// absolute.
func (a *Adapter) RunInherit(ctx context.Context, stdout, stderr io.Writer, args ...string) (int, error) {
	dir, err := os.MkdirTemp("", "forge-ci-trivy-*")
	if err != nil {
		return -1, fmt.Errorf("create trivy working directory: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }()

	cmd := safeexec.Command(ctx, a.bin(), slices.Concat(args, []string{"--config="})...)
	cmd.Env = safeexec.GateToolEnv(os.Environ())
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	if err == nil {
		return 0, nil
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode(), nil
	}

	// A non-exit failure (most commonly trivy missing from PATH) is a
	// missing prerequisite, not an internal bug: EX_CONFIG (78), not the
	// unclassified EX_SOFTWARE (70).
	return -1, safeexec.WrapError(err, a.bin(), safeexec.FirstArg(args))
}

func (a *Adapter) bin() string {
	if a.Bin != "" {
		return a.Bin
	}

	return "trivy"
}
