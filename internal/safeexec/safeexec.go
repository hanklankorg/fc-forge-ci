// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package safeexec wraps os/exec for the case where the binary path is
// a trusted/controlled value — a hardcoded default tool name like
// "git", an adapter field set by the CLI binding (e.g. trivy.Bin),
// or a path the operator passed via a typed CLI flag.
//
// The single //nolint:gosec G204 directive lives here so every adapter
// call site can drop its own. Callers must keep the contract: never
// pass a binary path derived directly from untrusted input (user-typed
// raw strings, headers, env values the user can inject) — only typed
// adapter fields, package constants, or pre-validated paths.
package safeexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// cancelGrace bounds how long Wait blocks after the context is cancelled
// (Ctrl-C, timeout) or the process exits with I/O still open. exec sends
// SIGKILL to the direct child immediately on cancel, but a grandchild
// (e.g. git-remote-https stuck in connect) can inherit the stdout/stderr
// pipe and keep Wait blocking forever. WaitDelay caps that: after the
// grace, exec force-closes the pipes and kills the process so Wait
// returns and the command is cancellable. Generous enough not to truncate
// a slow legitimate flush; a second Ctrl-C force-quits instantly anyway.
const cancelGrace = 10 * time.Second

// Command builds an *exec.Cmd for ctx using a trusted binary path and
// typed arguments. Equivalent to exec.CommandContext with the gosec
// G204 audit concentrated to this single site.
//
// Read the package doc before adding new call sites.
// The whole purpose of this package is to concentrate the
// non-static-command audit at this one site. Callers must pass a
// trusted/controlled bin per the package doc; the contract is
// upheld by the adapter layer.
func Command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	//nolint:gosec // by package contract: bin is trusted/controlled — see package doc.
	cmd := exec.CommandContext(ctx, bin, args...) // nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	cmd.WaitDelay = cancelGrace
	configureProcessGroup(cmd)

	return cmd
}

// WrapError classifies a subprocess error from cmd.Run / cmd.CombinedOutput
// into the project's sentinel ladder so callers don't have to spell
// the policy out at every adapter:
//
//   - nil                    → nil (passthrough)
//   - exec.ErrNotFound       → ErrPrerequisite (exit 78)
//     "operator forgot to install the toolchain"
//   - fs.ErrNotExist         → ErrPrerequisite (exit 78)
//     "an explicitly selected executable could not start"
//   - any other error        → ErrValidation (exit 1)
//     "the work the user asked for failed"
//
// bin is the trusted binary name. action is a short label (e.g. the
// leading positional arg "test" / "build" / "publish") that goes into
// the message — pass "" to omit and just blame the binary. The
// subprocess's own stdout/stderr is already streamed to the caller,
// so this helper deliberately does NOT dump the full argv into the
// wrap.
func WrapError(err error, bin, action string) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s not found in $PATH: %w", bin, errs.ErrPrerequisite)
	}

	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s could not start: %w: %w", bin, err, errs.ErrPrerequisite)
	}

	if action == "" {
		return fmt.Errorf("%s: %w: %w", bin, err, errs.ErrValidation)
	}

	return fmt.Errorf("%s %s: %w: %w", bin, action, err, errs.ErrValidation)
}

// ExitFailure classifies a tool that ran and exited with a failure from the
// tool's own error report, which the caller selects (see errs.ToolFailure):
// a host the tool could not reach is the dependency being unavailable, and
// every other failure is the work's own, as WrapError has it.
//
// An unavailable dependency carries the line of the report that states it,
// redacted, so the engine's own error names the host as every other such
// error does. The tool's log above it is not the engine's word.
//
// The class can also be the rate limit, when the report states a 429 and no
// other answer: errs.ToolFailure gives it for every tool read this way.
//
// A tool that was killed, by a time limit, a cancelled context or the
// kernel, left a log that stops anywhere, and no class is read from part of
// a report: it keeps WrapError's class. A tool that did not start is
// WrapError's to classify as well: the architecture guard reads the function
// that runs the process for that.
func ExitFailure(err error, bin, action, report string) error {
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == -1 {
		return WrapError(err, bin, action)
	}

	if action != "" {
		bin += " " + action
	}

	class := errs.ToolFailure(report)
	if errors.Is(class, errs.ErrDependencyUnavailable) {
		for line := range strings.SplitSeq(report, "\n") {
			if _, _, found := errs.TransportEvidence(line); found {
				return fmt.Errorf("%s: %w: %s: %w", bin, err, RedactKeyMaterial([]byte(strings.TrimSpace(line))), class)
			}
		}
	}

	return fmt.Errorf("%s: %w: %w", bin, err, class)
}

// KeepStdout makes cmd write its standard output to said as well, and
// KeepStderr its standard error, for a report to be read from. The stream
// is set before the call. A caller that gave one writer for both streams
// still gives one, and said then keeps both: exec writes to a writer from a
// single goroutine only when the two streams are equal.
//
// A kept stream reaches the caller's writer through a pipe and a goroutine
// of exec, also when that writer is a file the tool would otherwise have
// written to itself. What is kept is held in memory, whole, until the
// caller drops it.
func KeepStdout(cmd *exec.Cmd, said io.Writer) { keep(cmd, &cmd.Stdout, said) }

// KeepStderr is KeepStdout for the standard error.
func KeepStderr(cmd *exec.Cmd, said io.Writer) { keep(cmd, &cmd.Stderr, said) }

func keep(cmd *exec.Cmd, stream *io.Writer, said io.Writer) {
	// Two streams that are both unset go nowhere, and are not one writer.
	shared := cmd.Stdout != nil && sameWriter(cmd.Stdout, cmd.Stderr)

	if *stream == nil {
		*stream = said
	} else {
		*stream = io.MultiWriter(*stream, said)
	}

	if shared {
		cmd.Stdout, cmd.Stderr = *stream, *stream
	}
}

// sameWriter reports whether two streams are one writer. Writers of a type
// that cannot be compared are not, as in os/exec.
func sameWriter(a, b io.Writer) bool {
	if a != nil && !reflect.TypeOf(a).Comparable() {
		return false
	}

	return a == b
}

// FirstArg returns the leading positional argument, or "" when args is
// empty. It is the conventional source of the `action` label passed to
// WrapError (the subcommand verb: "test" / "build" / "publish"), shared
// by every exec adapter so the extraction lives in one place.
func FirstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}

	return args[0]
}
