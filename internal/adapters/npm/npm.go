// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package npm shells out to the system `npm` binary. Used by the
// version-bump use case to run `npm version <v> --no-git-tag-version`.
package npm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// Adapter wraps the npm binary. Bin is overridable for tests.
type Adapter struct {
	Bin string   // empty → "npm"
	Env []string // nil inherits; an explicit base scopes dependency credentials
}

// Run captures stdout/stderr from `npm` while still returning the command
// failure so callers can distinguish expected npm failures from parse errors.
func (a *Adapter) Run(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := safeexec.Command(ctx, a.bin(), args...)
	cmd.Dir = dir
	cmd.Env = a.Env

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stdout.String(), stderr.String(), safeexec.WrapError(err, a.bin(), safeexec.FirstArg(args))
	}

	return stdout.String(), stderr.String(), nil
}

// New returns an Adapter using the system npm.
func New() *Adapter { return &Adapter{} }

// NewNpx returns an Adapter that drives `npx` instead of `npm`, for running
// one-shot pinned tools (e.g. cyclonedx-npm) from the npm toolchain.
//
//nolint:funcorder // grouped with New (the established constructor placement in this file).
func NewNpx() *Adapter { return &Adapter{Bin: "npx"} }

// RunInherit invokes `npm` with args and streams stdout/stderr to the
// provided writers. The bash uses npm directly; this matches the shape.
func (a *Adapter) RunInherit(ctx context.Context, dir string, stdout, stderr io.Writer, args ...string) error {
	return a.RunWithEnv(ctx, dir, a.Env, stdout, stderr, args...)
}

// RunWithEnv invokes npm with an explicitly selected environment for one phase.
//
// An `npm ci` that could not reach its registry ends as an unavailable
// dependency, by the error code npm prints (errs.NPMFailure). The class of
// every other command is left alone: `npm test` and `npm run` end on what a
// script of the project printed, which may be another npm's error code.
func (a *Adapter) RunWithEnv(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, args ...string) error {
	cmd := safeexec.Command(ctx, a.bin(), args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	action := safeexec.FirstArg(args)

	// Only what `npm ci` says is kept and read.
	var said bytes.Buffer

	if action == "ci" {
		safeexec.KeepStderr(cmd, &said)
	}

	err := cmd.Run()
	if err == nil {
		return nil
	}

	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		if class := errs.NPMFailure(said.String()); errs.Retriable(class) {
			return fmt.Errorf("%s %s: %w: %w", a.bin(), action, err, class)
		}
	}

	return safeexec.WrapError(err, a.bin(), action)
}

func (a *Adapter) bin() string {
	if a.Bin != "" {
		return a.Bin
	}

	return "npm"
}
