// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build !linux

package safeexec_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/safeexec"
)

func TestSwapPolicyIsExplicitlyUndeterminedOutsideLinux(t *testing.T) {
	t.Parallel()

	enabled, err := safeexec.SwapEnabled()
	if err != nil || enabled {
		t.Fatalf("SwapEnabled() = %t, %v; want false, nil", enabled, err)
	}

	state, _, err := safeexec.RequireNoSwap(true)
	if err != nil || state != safeexec.SwapUndetermined {
		t.Fatalf("RequireNoSwap(true) = %s, %v; want undetermined, nil", state, err)
	}
}
