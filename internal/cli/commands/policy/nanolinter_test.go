// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package policy

import (
	"errors"
	"io"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestNanolinterTypedModeRefusesAmbiguousOrMalformedInputs(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--offline=yes"},
		{"--offline=1"},
		{"--offline="},
		{"--required-lints=secrets", "--", "must-not-execute"},
		{"--disable-checks=osv", "--", "must-not-execute"},
	} {
		command := nanolinterCmd()
		command.Writer, command.ErrWriter = io.Discard, io.Discard

		err := command.Run(t.Context(), append([]string{"nanolinter"}, args...))
		if !errors.Is(err, errs.ErrUsage) {
			t.Fatalf("args %q: got %v, want usage refusal before command execution", args, err)
		}
	}

	// No arguments retains the existing explicit-child-command contract.
	err := nanolinterCmd().Run(t.Context(), []string{"nanolinter"})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("legacy no-command invocation = %v, want usage error", err)
	}
}
