// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"strconv"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// NotCoveredf reports a scenario that cannot run with the selected providers.
// Only an explicitly focused run may skip it; a full run must retain its
// coverage requirement. Call this for absent prerequisites, never after a
// selected provider fails an assertion.
func NotCoveredf(tb TB, format string, args ...any) {
	tb.Helper()

	if os.Getenv(liveProfileEnv) == profileFocused {
		tb.Skipf(format, args...)

		return
	}

	tb.Fatalf(format, args...)
}

// Need is one thing a scenario needs from the environment it was pointed at.
//
// Environment facts used to be consulted three different ways: hardcoded
// predicates that could not know the environment, one hand-rolled accessor per
// fact, and late skips buried inside helpers. Each new fact then cost a new
// accessor plus a call-site edit in every scenario that cared — ten of them, for
// the runner question alone — and a scenario that forgot one did not fail. It
// waited out a timeout and reported something else.
//
// So the questions are named, and Requires answers them in one place. Adding a
// fact is a Need plus a case, not an edit everywhere.
type Need int

const (
	// NeedsInRunner is a job this suite can drive on a runner that exists.
	// Two facts on purpose: whether the SUITE implements an in-runner probe
	// for the forge, and whether the CONTRACT says a runner is deployed for it
	// (the workflow-runs capability).
	NeedsInRunner Need = iota

	// NeedsFulcio is a certificate authority that will issue for this forge:
	// one deployed at all, and configured to trust this issuer.
	NeedsFulcio

	// NeedsPackages requires the contract to advertise a package registry.
	NeedsPackages
)

// Requires reports whether forge satisfies every need, logging the first that
// does not.
//
// Logf rather than Skipf: these run inside a loop over forges, where skipping
// the test would abandon the forges after this one. The caller continues.
//
// Each message names the contract field that decides it, so a reader is sent to
// the environment's own description of itself rather than to a symptom. That is
// the whole reason this exists: an unmet need used to surface as a queued job or
// an opaque cosign error, both of which read as product defects.
func Requires(tb TB, forge provider.ForgeAPI, needs ...Need) bool {
	tb.Helper()

	scope, err := liveProviderScope()
	if err != nil {
		tb.Fatalf("livetest: %v", err)
		return false
	}
	if scope != "" && scope != forge {
		return false
	}

	contract, err := currentLabContract()
	if err != nil {
		tb.Fatalf("livetest: live-target contract: %v", err)

		return false
	}

	for _, need := range needs {
		switch need {
		case NeedsInRunner:
			if !RunsInRunner(forge) {
				tb.Logf("SKIP %s: the in-runner tier does not drive this forge yet", forge)

				return false
			}

			endpoint, endpointErr := contract.Endpoint(labcontract.Kind(forge))
			if endpointErr != nil {
				tb.Fatalf("livetest: %v", endpointErr)

				return false
			}

			if !endpoint.HasCapability(labcontract.CapabilityWorkflowRuns) {
				tb.Logf("SKIP %s: the contract deployed no runner for endpoint %q (capability workflow-runs)", forge, endpoint.Name)

				return false
			}
		case NeedsFulcio:
			endpoint, err := contract.Endpoint(labcontract.Kind(forge))
			if err != nil {
				tb.Fatalf("livetest: %v", err)

				return false
			}

			fulcioURL, issuer, ok := contract.FulcioFor(endpoint.Name)
			if !ok {
				tb.Logf("SKIP %s: contract fulcio has no issuer mapping for endpoint %q", forge, endpoint.Name)

				return false
			}

			target := Target{Forge: forge, Host: endpoint.Host(), FulcioURL: fulcioURL, OIDCIssuer: issuer}
			if trustErr := validateTargetAuthorities(target); trustErr != nil {
				tb.Fatalf("livetest: Fulcio trust boundary refused %s: %v", forge, trustErr)

				return false
			}
		case NeedsPackages:
			endpoint, endpointErr := contract.Endpoint(labcontract.Kind(forge))
			if endpointErr != nil {
				tb.Fatalf("livetest: %v", endpointErr)

				return false
			}

			if !endpoint.HasCapability(labcontract.CapabilityPackages) {
				tb.Logf("SKIP %s: endpoint %q has no packages capability", forge, endpoint.Name)

				return false
			}
		default:
			// An unwired Need is a programming error, not a skip: silently
			// treating it as satisfied is how a gate stops gating.
			tb.Fatalf("livetest: unhandled %s; wire it into Requires", need)

			return false
		}
	}

	return true
}

// ForgesMeeting narrows forges to the ones this environment can satisfy, and
// skips the scenario when that leaves nothing.
//
// The gate is the LOOP SOURCE rather than a check inside the body, which is what
// makes two failure modes unreachable instead of merely handled. A scenario
// cannot iterate a forge it has no way to drive, and it cannot report PASS
// having covered nothing — the shape that costs the most, because a failure gets
// investigated and a skip gets read, while a green that exercised nothing is
// simply trusted. Keyless did exactly that on the k3s road: both forges gated
// out, zero subtests, PASS in 0.00s.
//
// Skipf rather than Errorf: covering nothing is a fact about the ENVIRONMENT,
// not a defect. The k3s road genuinely runs no Fulcio, and demanding one there
// would make the road unusable rather than honest.
func ForgesMeeting(tb TB, forges []provider.ForgeAPI, needs ...Need) []provider.ForgeAPI {
	tb.Helper()

	meeting := make([]provider.ForgeAPI, 0, len(forges))

	for _, forge := range forges {
		if Requires(tb, forge, needs...) {
			meeting = append(meeting, forge)
		}
	}

	if len(meeting) == 0 {
		tb.Skipf("no forge in this environment meets %v, so this scenario would cover nothing", needs)
	}

	return meeting
}

// String names the need, so an unwired one reports what it is rather than which
// integer it happens to be. The diagnostic is the whole reason this file exists;
// it would be poor to make the file's own failure illegible.
func (n Need) String() string {
	switch n {
	case NeedsInRunner:
		return "NeedsInRunner"
	case NeedsFulcio:
		return "NeedsFulcio"
	case NeedsPackages:
		return "NeedsPackages"
	default:
		return "Need(" + strconv.Itoa(int(n)) + ")"
	}
}

// allNeeds is every Need, so a test can assert Requires handles each one. The
// exhaustiveness guard the default case cannot provide on its own: a new Need
// that nobody wired would otherwise only fail the first time a scenario used it.
func allNeeds() []Need {
	return []Need{NeedsInRunner, NeedsFulcio, NeedsPackages}
}
