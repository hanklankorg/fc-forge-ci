// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package safeexec_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// TestSwapState_NamesEveryState pins the platform-independent label each state
// renders. Keeping it outside the Linux fixture also exercises the diagnostic
// surface compiled into the Darwin release.
func TestSwapState_NamesEveryState(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		state safeexec.SwapState
		want  string
	}{
		{safeexec.SwapAbsent, "absent"},
		{safeexec.SwapUndetermined, "undetermined"},
		{safeexec.SwapBypassed, "bypassed"},
		{safeexec.SwapOverridden, "overridden"},
		{safeexec.SwapCgroupDisabled, "cgroup-disabled"},
	} {
		if got := tc.state.String(); got != tc.want {
			t.Errorf("SwapState(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}

	seen := map[string]bool{}

	for _, state := range []safeexec.SwapState{safeexec.SwapAbsent, safeexec.SwapUndetermined, safeexec.SwapBypassed, safeexec.SwapOverridden, safeexec.SwapCgroupDisabled} {
		label := state.String()
		if seen[label] {
			t.Errorf("two states render the same label %q", label)
		}

		seen[label] = true
	}

	if got := safeexec.SwapState(99).String(); got != "unknown" {
		t.Errorf("unnamed state rendered %q, want %q", got, "unknown")
	}
}
