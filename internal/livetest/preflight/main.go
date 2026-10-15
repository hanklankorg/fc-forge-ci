// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Command preflight validates the live tier's inputs without touching a
// provider: the contract for this suite's lab account and its CA. The run
// script calls it before building anything, so a missing or stale contract is
// reported in seconds with where the suite looked, and an accepted one is
// explained: endpoints, capabilities, credential lifetime and the run identity
// the suite's prefix derives.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: preflight (reads the lab contract for account rc, LAB_TARGETS_FILE when set, and RC_LIVE_PROFILE)")
		os.Exit(2)
	}

	summary, err := livetest.CheckLiveInputs(time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "live preflight: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("live preflight: %d provider(s), generation %s, namespace %s\n\n%s",
		summary.Selected, summary.Generation, livetest.ResourcePrefix, summary.Explanation)
}
