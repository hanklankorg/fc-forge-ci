// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"os"
)

const (
	// livePreflightEnv makes a live run a preflight: each scenario runs its
	// admission, the checks it makes before any effect, and stops there.
	livePreflightEnv = "FORGE_CI_LIVE_PREFLIGHT"
	// liveAdmittedPrefix marks, in the run's log, a scenario whose admission
	// a preflight run completed.
	liveAdmittedPrefix = "FORGE_CI_LIVE_ADMITTED "
	// verdictAdmitted is a preflight run in which every scenario that ran
	// was admitted.
	verdictAdmitted = "admitted"
)

// errPreflightEffect refuses a lab client in a preflight run: an admission
// reads the contract, the host and anonymous registries, never the lab with
// its credential.
var errPreflightEffect = errors.New("a preflight run reached a lab effect; end the scenario's admission with Admitted before its first effect")

// PreflightRun reports whether this live run is a preflight.
func PreflightRun() bool {
	return os.Getenv(livePreflightEnv) == "1"
}

// RegisterAdmittedScenario is RegisterLiveScenario for a scenario that ends
// its admission with Admitted. Only such a scenario runs in a preflight run;
// it runs there until Admitted.
func RegisterAdmittedScenario(t interface{ Name() string }) {
	fmt.Printf("%s%s\n", liveScenarioPrefix, t.Name())
}

// Admitted ends a scenario's admission: every check it makes before any
// effect is behind it. A preflight run reports the scenario admitted and
// stops it here; any other run goes on.
func Admitted(tb TB) {
	tb.Helper()

	if !PreflightRun() {
		return
	}

	if named, ok := tb.(interface{ Name() string }); ok {
		fmt.Printf("%s%s\n", liveAdmittedPrefix, named.Name())
	}

	tb.Skipf("preflight: admitted; the run would go on to its first effect here")
}

// skipUnadmittedInPreflight stops, in a preflight run, a scenario that
// declares no admission boundary: nothing says where its effects begin, so
// it does not run at all.
func skipUnadmittedInPreflight(t any) {
	if !PreflightRun() {
		return
	}

	skipper, ok := t.(interface {
		Skipf(format string, args ...any)
	})
	if !ok {
		panic("livetest: a preflight run cannot stop this scenario")
	}

	skipper.Skipf("preflight: this scenario declares no admission boundary (RegisterAdmittedScenario and Admitted), so a preflight cannot run it without effects")
}
