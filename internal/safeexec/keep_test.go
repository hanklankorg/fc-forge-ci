// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package safeexec_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

const bothStreams = "echo out; echo err >&2"

// A kept stream still reaches the caller's writer, and the other stream is
// not kept.
func TestKeepStderr_KeepsTheStreamBesideTheCallersWriter(t *testing.T) {
	t.Parallel()

	var stdout, stderr, said bytes.Buffer

	cmd := safeexec.Command(t.Context(), "sh", "-c", bothStreams)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	safeexec.KeepStderr(cmd, &said)

	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	if stdout.String() != "out\n" || stderr.String() != "err\n" || said.String() != "err\n" {
		t.Errorf("stdout = %q, stderr = %q, kept = %q", stdout.String(), stderr.String(), said.String())
	}
}

// A caller that gave one writer for both streams still gives one, so exec
// keeps writing to it from a single goroutine, and both streams are kept.
// Two streams that are unset are not one writer: only the kept one is set.
func TestKeepStderr_OneWriterForBothStreamsStaysOne(t *testing.T) {
	t.Parallel()

	var log, said bytes.Buffer

	shared := safeexec.Command(t.Context(), "sh", "-c", bothStreams)
	shared.Stdout, shared.Stderr = &log, &log
	safeexec.KeepStderr(shared, &said)

	if err := shared.Run(); err != nil {
		t.Fatal(err)
	}

	if shared.Stdout != shared.Stderr || log.String() != "out\nerr\n" || said.String() != "out\nerr\n" {
		t.Errorf("one writer: same = %v, log = %q, kept = %q", shared.Stdout == shared.Stderr, log.String(), said.String())
	}

	said.Reset()

	unset := safeexec.Command(t.Context(), "sh", "-c", bothStreams)
	safeexec.KeepStderr(unset, &said)

	if err := unset.Run(); err != nil {
		t.Fatal(err)
	}

	if unset.Stdout != nil || said.String() != "err\n" {
		t.Errorf("unset streams: stdout set = %v, kept = %q", unset.Stdout != nil, said.String())
	}
}

// A tool that exited with a failure takes the class of its report. One that
// was killed keeps the class of a failed command: its log stops anywhere.
func TestExitFailure_AKilledToolIsNotClassifiedFromItsLog(t *testing.T) {
	t.Parallel()

	const report = `Get "https://registry.example/v2/": dial tcp: lookup registry.example: no such host`

	for script, want := range map[string]error{"exit 3": errs.ErrDependencyUnavailable, "kill -9 $$": errs.ErrValidation} {
		err := safeexec.Command(t.Context(), "sh", "-c", script).Run()
		if err == nil {
			t.Fatalf("%s: the command did not fail", script)
		}

		if got := safeexec.ExitFailure(err, "tool", "fetch", report); !errors.Is(got, want) || errs.ExitCodeFromError(got) != errs.ExitCodeFromError(want) {
			t.Errorf("%s: ExitFailure = %v, want %v", script, got, want)
		}
	}
}
