// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package mockbinary_test

import (
	"os/exec"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

// TestMock_EmptyScriptSucceedsAndStillRecords: an empty body is the common
// "any call is fine" stub, and it must exit zero rather than fail to parse.
func TestMock_EmptyScriptSucceedsAndStillRecords(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("tool", "")

	out, err := exec.CommandContext(t.Context(), m.Path("tool"), "--flag").CombinedOutput() //nolint:gosec // mockbinary stub path is test-generated.
	if err != nil {
		t.Fatalf("empty stub failed: %v\n%s", err, out)
	}

	if len(out) != 0 {
		t.Errorf("empty stub wrote %q", out)
	}

	if calls := m.Invocations("tool"); len(calls) != 1 || len(calls[0].Args) != 1 || calls[0].Args[0] != "--flag" {
		t.Errorf("invocations = %#v, want one call with --flag", calls)
	}
}
