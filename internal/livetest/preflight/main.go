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
	"strconv"
	"time"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

func main() {
	if len(os.Args) == 4 && os.Args[1] == "classify-run" {
		status, err := strconv.Atoi(os.Args[2])
		if err != nil || status < 0 || status > 255 {
			usage()
		}
		var log *os.File
		if status == 0 {
			log, err = os.Open(os.Args[3]) //nolint:gosec // Explicit operator-owned retained log.
			if err != nil {
				fmt.Fprintf(os.Stderr, "live result: %v\n", err)
				os.Exit(2)
			}
			defer func() { _ = log.Close() }()
		}
		result, err := livetest.ClassifyLiveRun(log, status)
		if err != nil {
			fmt.Fprintf(os.Stderr, "live result: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("%d %s\n", result.ExitCode, result.Verdict)
		return
	}
	// `preflight runner-arch` prints only the architecture the lab's runners
	// execute, which the run script builds the runner binaries for.
	// `preflight consumer-trial-layer` prints only the URL a Forgejo consumer
	// trial serves the layer under test from, which the lab's kaniko job image
	// names as its source.
	if len(os.Args) == 2 {
		var (
			fact string
			err  error
		)

		switch os.Args[1] {
		case "signing-inputs":
			err = livetest.CheckReusedSigningInputs(os.Getenv("FORGE_CI_LIVE_SIGNING_KEY_DIR"))
			fact = "reused signing input metadata accepted (key validity remains signer-verified)"
		case "runner-arch":
			fact, err = livetest.LabRunnerArch()
		case "consumer-trial-layer":
			fact, err = livetest.ConsumerTrialLayerURL()
		default:
			usage()
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "live preflight: %v\n", err)
			os.Exit(2)
		}

		fmt.Println(fact)

		return
	}

	if len(os.Args) != 1 {
		usage()
	}

	summary, err := livetest.CheckLiveInputs(time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "live preflight: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("live preflight: %d provider(s), generation %s, namespace %s\n\n%s",
		summary.Selected, summary.Generation, livetest.ResourcePrefix, summary.Explanation)
}

func usage() {
	fmt.Fprintln(os.Stderr, "       preflight classify-run <exit-code> <retained-log>")
	fmt.Fprintln(os.Stderr, "usage: preflight [signing-inputs | runner-arch | consumer-trial-layer] (signing-inputs checks only FORGE_CI_LIVE_SIGNING_KEY_DIR; other modes read the forgeci lab contract)")
	os.Exit(2)
}
