// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"strings"
	"testing"
)

// A preflight run passes when every scenario that ran was admitted and none
// failed; a scenario that skipped before its boundary, as one that declares
// none does, leaves the run unadmitted.
func TestReadLivePreflightRun(t *testing.T) {
	const (
		admitted   = "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\nFORGE_CI_LIVE_ADMITTED TestTrial\n--- SKIP: TestTrial (0s)"
		unbounded  = "=== RUN   TestOther\nFORGE_CI_LIVE_SCENARIO TestOther\n--- SKIP: TestOther (0s)"
		subtest    = "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/gitlab\nFORGE_CI_LIVE_ADMITTED TestTrial/gitlab\n--- SKIP: TestTrial/gitlab (0s)\n--- PASS: TestTrial (0s)"
		refused    = "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- FAIL: TestTrial (0s)"
		unexpected = "=== RUN   TestTrial\nFORGE_CI_LIVE_ADMITTED TestTrial\n--- SKIP: TestTrial (0s)"
		oneOfTwo   = "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/public\nFORGE_CI_LIVE_ADMITTED TestTrial/public\n--- SKIP: TestTrial/public (0s)\n" +
			"=== RUN   TestTrial/private\n--- SKIP: TestTrial/private (0s)\n--- PASS: TestTrial (0s)"
	)

	for _, tc := range []struct {
		name, log, verdict string
		status, want       int
	}{
		{"admitted", livePackageLog("one", admitted), "admitted", 0, 0},
		{"admitted in a subtest", livePackageLog("one", subtest), "admitted", 0, 0},
		{"one visibility of two admitted", livePackageLog("one", oneOfTwo), "unadmitted:", 0, 1},
		{"a scenario without a boundary", livePackageLog("one", admitted+"\n"+unbounded), "unadmitted:", 0, 1},
		{"an admission refused", livePackageLog("one", refused), "failed", 1, 1},
		{"nothing registered", livePackageLog("one", unexpected), "skipped:", 0, 1},
		{"interrupted", livePackageLog("one", admitted), "interrupted", 130, 130},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdict, err := ReadLivePreflightRun(strings.NewReader(tc.log), tc.status)
			got := verdict.Result()

			if err != nil || got.ExitCode != tc.want || !strings.HasPrefix(got.Verdict, tc.verdict) {
				t.Fatalf("classification: %+v, %v", got, err)
			}
		})
	}

	// The same log is no pass for an ordinary run: nothing passed.
	if got, err := ClassifyLiveRun(strings.NewReader(livePackageLog("one", admitted)), 0); err != nil || got.ExitCode == 0 {
		t.Fatalf("an admitted preflight passed as a run: %+v, %v", got, err)
	}
}

// namedTB is a recordingTB with a test name, as a scenario's has.
type namedTB struct{ recordingTB }

func (*namedTB) Name() string { return "TestTrial" }

// skipped is the one skip the TB recorded, or "" when it recorded none.
func (n *namedTB) skipped() string { return strings.Join(n.skips, "; ") }

// Outside a preflight run Admitted and registration change nothing; in one,
// an unbounded scenario is stopped at registration, an admitted one at
// Admitted, and the lab client is refused.
func TestPreflightRunStopsBeforeEffects(t *testing.T) {
	t.Run("an ordinary run", func(t *testing.T) {
		t.Setenv(livePreflightEnv, "")

		tb := &namedTB{}
		RegisterLiveScenario(tb)
		Admitted(tb)

		if tb.skipped() != "" {
			t.Fatalf("an ordinary run skipped: %s", tb.skipped())
		}
	})

	t.Run("a preflight run", func(t *testing.T) {
		t.Setenv(livePreflightEnv, "1")

		unbounded := &namedTB{}
		RegisterLiveScenario(unbounded)

		if !strings.Contains(unbounded.skipped(), "declares no admission boundary") {
			t.Fatalf("an unbounded scenario ran: %q", unbounded.skipped())
		}

		bounded := &namedTB{}
		RegisterAdmittedScenario(bounded)

		if bounded.skipped() != "" {
			t.Fatalf("an admitted scenario was stopped at registration: %s", bounded.skipped())
		}

		Admitted(bounded)

		if !strings.Contains(bounded.skipped(), "preflight: admitted") {
			t.Fatalf("Admitted did not stop the scenario: %q", bounded.skipped())
		}

		if _, err := scratchClient(Target{}); !errors.Is(err, errPreflightEffect) {
			t.Fatalf("a preflight run got a lab client: %v", err)
		}
	})
}
