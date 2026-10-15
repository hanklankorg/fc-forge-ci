// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package trivy wraps the system `trivy` binary. The runtime image
// bakes trivy in; local runs need it on PATH.
package trivy

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// Adapter wraps the trivy binary. Bin is overridable for tests.
type Adapter struct {
	Bin string // empty → "trivy"
	// UnsetEnv removes credentials from the trivy environment. Trivy reads
	// registry credentials from the auth file the job logged in with, and
	// otherwise authenticates to ghcr.io with GITHUB_TOKEN. On a Forgejo job
	// that variable holds the job's forge token, so a vulnerability-database
	// fallback to ghcr.io would send the forge token there and be denied.
	UnsetEnv []string
}

// New returns an Adapter using the system trivy, removing unsetEnv from its
// environment.
func New(unsetEnv ...string) *Adapter { return &Adapter{UnsetEnv: unsetEnv} }

// RunInherit invokes `trivy` with args, streaming stdout/stderr.
// Returns the process exit code and any error from starting/waiting.
func (a *Adapter) RunInherit(ctx context.Context, stdout, stderr io.Writer, args ...string) (int, error) {
	cmd := safeexec.Command(ctx, a.bin(), args...)
	if len(a.UnsetEnv) > 0 {
		cmd.Env = safeexec.EnvWithout(os.Environ(), a.UnsetEnv...)
	}

	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err == nil {
		return 0, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}

	// A non-exit failure (most commonly trivy missing from PATH) is an
	// external-dependency problem, not an internal bug — classify it so it
	// surfaces as EX_UNAVAILABLE (69) rather than the unclassified
	// EX_SOFTWARE (70).
	return -1, safeexec.WrapError(err, a.bin(), safeexec.FirstArg(args))
}

func (a *Adapter) bin() string {
	if a.Bin != "" {
		return a.Bin
	}

	return "trivy"
}
