// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// sprintf keeps the recording TB's methods to one line each.
func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// recordingTB is the smallest TB that keeps a logged skip, a test skip, an
// error and a fatal apart. Logf, Skipf and Errorf used to share one slice, so
// a gate that skipped the whole test where it should only log, or errored
// where it should skip, looked the same as the right behaviour.
type recordingTB struct {
	logs   []string
	skips  []string
	errors []string
	fatal  string
}

func (r *recordingTB) Helper()                  {}
func (r *recordingTB) Context() context.Context { return context.Background() }
func (r *recordingTB) Cleanup(func())           {}
func (r *recordingTB) Logf(format string, args ...any) {
	r.logs = append(r.logs, sprintf(format, args...))
}
func (r *recordingTB) Skipf(format string, args ...any) {
	r.skips = append(r.skips, sprintf(format, args...))
}
func (r *recordingTB) Errorf(format string, args ...any) {
	r.errors = append(r.errors, sprintf(format, args...))
}
func (r *recordingTB) Fatalf(format string, args ...any) { r.fatal = sprintf(format, args...) }

func TestNotCovered_OnlyExplicitFocusedRunsMaySkip(t *testing.T) {
	for _, profile := range []string{"", "full", "focused", "focusd"} {
		t.Run(profile, func(t *testing.T) {
			t.Setenv(liveProfileEnv, profile)

			tb := &recordingTB{}
			NotCoveredf(tb, "need %d providers for %s", 2, "parity")

			const message = "need 2 providers for parity"
			if profile == "focused" {
				if !slices.Equal(tb.skips, []string{message}) || tb.fatal != "" {
					t.Fatalf("focused coverage = %+v; want one explicit skip", tb)
				}
			} else if tb.fatal != message || len(tb.skips) != 0 {
				t.Fatalf("profile %q coverage = %+v; want a failure", profile, tb)
			}
		})
	}
}

// withoutWorkflowRuns removes the deployed-runner capability from every
// endpoint of the compose fixture.
func withoutWorkflowRuns(t *testing.T, body string) string {
	t.Helper()

	const capability = ",\n        \"workflow-runs\""
	if !strings.Contains(body, capability) {
		t.Fatalf("fixture no longer declares %q", capability)
	}

	return strings.ReplaceAll(body, capability, "")
}

// withoutGitLabIssuer removes the GitLab Fulcio issuer mapping from the
// compose fixture.
func withoutGitLabIssuer(t *testing.T, body string) string {
	t.Helper()

	return replaceInFixture(t, body, `,
      {
        "endpoint": "gitlab",
        "oidc_issuer": "https://gitlab.compose.forgelab:8443"
      }`, "")
}

// Every Need must be wired into Requires. Without this, a Need added but not
// handled would fall to the default case and only be noticed the first time a
// scenario asked for it — during a live run, against a real forge.
func TestRequires_HandlesEveryNeed(t *testing.T) {
	setCurrentContract(t, writeContract(t, composeFixtureBody(t)))

	for _, need := range allNeeds() {
		tb := &recordingTB{}
		if !Requires(tb, provider.ForgeGitLab, need) || tb.fatal != "" || len(tb.logs) != 0 {
			t.Errorf("Need %s must be satisfied by the complete fixture: %+v", need, tb)
		}
	}
}

// Pin identities and uniqueness independently of both the production list and
// Requires; a duplicate cannot stand in for a missing Need. The contiguous-iota
// probe additionally catches a newly handled Need beyond this inventory.
func TestAllNeeds_ListsEveryNeed(t *testing.T) {
	setCurrentContract(t, writeContract(t, composeFixtureBody(t)))

	want := []Need{NeedsInRunner, NeedsFulcio}
	got := allNeeds()

	counts := make(map[Need]int)
	for _, need := range got {
		counts[need]++
	}

	if len(got) != len(want) {
		t.Errorf("allNeeds() = %v, want exactly %v", got, want)
	}

	for _, need := range want {
		if counts[need] != 1 {
			t.Errorf("allNeeds() lists %s %d times, want once", need, counts[need])
		}
	}

	past := Need(len(want))

	tb := &recordingTB{}
	Requires(tb, provider.ForgeGitLab, past)

	if tb.fatal == "" {
		t.Errorf("Need %d is handled by Requires but missing from allNeeds(): "+
			"append it, or TestRequires_HandlesEveryNeed silently stops covering it", int(past))
	}
}

// An unmet need must say which contract field decides it, so the reader is sent
// to the environment's description of itself rather than to a symptom.
func TestRequires_NamesTheContractField(t *testing.T) {
	for _, tc := range []struct {
		need Need
		body func(*testing.T, string) string
		want string
	}{
		{NeedsInRunner, withoutWorkflowRuns, `endpoint "gitlab" (capability workflow-runs)`},
		{NeedsFulcio, withoutGitLabIssuer, `contract fulcio has no issuer mapping for endpoint "gitlab"`},
	} {
		setCurrentContract(t, writeContract(t, tc.body(t, composeFixtureBody(t))))

		tb := &recordingTB{}
		if Requires(tb, provider.ForgeGitLab, tc.need) {
			t.Errorf("Need %s was satisfied by a contract without it", tc.need)

			continue
		}

		if len(tb.logs) == 0 || !strings.Contains(tb.logs[0], tc.want) {
			t.Errorf("Need %s skipped without naming %s: %v", tc.need, tc.want, tb.logs)
		}
	}
}

// TestRequires_EachOutcomeIsDistinct walks Requires and ForgesMeeting through
// every outcome with the recording TB: satisfied, an unmet need that only
// logs (a forge the suite cannot drive, a runner not deployed, no issuer
// mapping), several needs stopping at the first unmet one, and the empty
// narrowing that skips the scenario instead of passing with nothing covered.
// Logged skips never skip the test and never fail it.
func TestRequires_EachOutcomeIsDistinct(t *testing.T) {
	full := composeFixtureBody(t)

	for name, tc := range map[string]struct {
		body     string
		forge    provider.ForgeAPI
		needs    []Need
		want     bool
		wantLogs []string
	}{
		"every need met": {body: full, forge: provider.ForgeGitLab, needs: []Need{NeedsInRunner, NeedsFulcio}, want: true},
		"forge the suite cannot drive": {
			body: full, forge: provider.ForgeGitHub, needs: []Need{NeedsInRunner},
			wantLogs: []string{"SKIP github: the in-runner tier does not drive this forge yet"},
		},
		"runner not deployed": {
			body: withoutWorkflowRuns(t, full), forge: provider.ForgeGitLab, needs: []Need{NeedsInRunner, NeedsFulcio},
			wantLogs: []string{`SKIP gitlab: the contract deployed no runner for endpoint "gitlab" (capability workflow-runs)`},
		},
		"no issuer mapping": {
			body: withoutGitLabIssuer(t, full), forge: provider.ForgeGitLab, needs: []Need{NeedsInRunner, NeedsFulcio},
			wantLogs: []string{`SKIP gitlab: contract fulcio has no issuer mapping for endpoint "gitlab"`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			setCurrentContract(t, writeContract(t, tc.body))

			tb := &recordingTB{}

			got := Requires(tb, tc.forge, tc.needs...)
			if got != tc.want || tb.fatal != "" || len(tb.skips) != 0 || len(tb.errors) != 0 || !slices.Equal(tb.logs, tc.wantLogs) {
				t.Errorf("Requires = %v, tb = %+v; want %v with logs %q only", got, tb, tc.want, tc.wantLogs)
			}
		})
	}

	t.Run("narrowing keeps only the forges that meet every need", func(t *testing.T) {
		setCurrentContract(t, writeContract(t, full))

		tb := &recordingTB{}

		got := ForgesMeeting(tb, []provider.ForgeAPI{provider.ForgeGitHub, provider.ForgeGitLab}, NeedsInRunner)
		if !slices.Equal(got, []provider.ForgeAPI{provider.ForgeGitLab}) || len(tb.logs) != 1 || len(tb.skips) != 0 || tb.fatal != "" {
			t.Errorf("ForgesMeeting = %v, tb = %+v; want only gitlab with one logged skip", got, tb)
		}
	})

	t.Run("narrowing to nothing skips the scenario", func(t *testing.T) {
		setCurrentContract(t, writeContract(t, withoutWorkflowRuns(t, full)))

		tb := &recordingTB{}

		got := ForgesMeeting(tb, []provider.ForgeAPI{provider.ForgeGitHub, provider.ForgeGitLab}, NeedsInRunner)
		if len(got) != 0 || len(tb.logs) != 2 || len(tb.skips) != 1 || !strings.Contains(tb.skips[0], "would cover nothing") || tb.fatal != "" || len(tb.errors) != 0 {
			t.Errorf("ForgesMeeting = %v, tb = %+v; want an empty result, two logged skips and one test skip", got, tb)
		}
	})

	t.Run("a forge absent from the contract is fatal, not a skip", func(t *testing.T) {
		setCurrentContract(t, writeContract(t, fixtureBody(t, "neutral-targets-v4-github.json")))

		tb := &recordingTB{}
		if Requires(tb, provider.ForgeGitLab, NeedsInRunner) || !strings.Contains(tb.fatal, "gitlab: no endpoint of that kind") || len(tb.logs) != 0 {
			t.Fatalf("Requires on a contract without the forge: %+v; want a fatal naming the endpoint", tb)
		}
	})
}
