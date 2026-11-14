// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"strings"
	"testing"
)

// What a cause is read from and what it changes: the engine's own error in
// the trace of a failed job gives it, and the test stays failed in a run that
// stays failed. Nothing else in a trace does: not a job that passed, not a
// line a program of the job printed, not the harness's own calls.
func TestReadLiveRunNamesTheCauseOnlyFromAFailedJobsEngineError(t *testing.T) {
	t.Parallel()

	const (
		unreached = `        2026-11-11T12:23:38.9347345Z Error: download https://github.com/o/r/t.tar.gz: Get "https://github.com/o/r/t.tar.gz": dial tcp: lookup github.com on 10.201.0.1:53: read udp 10.201.13.222:52721->10.201.0.1:53: i/o timeout: dependency unavailable`
		printed   = `        2026-11-11T09:56:02.1363330Z [INFO] Building Could not resolve host: demo.example 1.0`
		dialled   = `        2026-11-11T09:56:03.1363330Z 01O --- FAIL: TestDial: dial tcp 127.0.0.1:9: connect: connection refused`
	)

	log := strings.Join([]string{
		"FORGE_CI_LIVE_PACKAGE_BEGIN /src/test/live/conformance",
		"=== RUN   TestTrial",
		"FORGE_CI_LIVE_SCENARIO TestTrial",
		"=== RUN   TestTrial/unreached",
		"    trial_test.go:66: the end of the failed job's trace:",
		unreached,
		"        ERROR: Job failed: exit code 69",
		"=== RUN   TestTrial/scan",
		"    trial_test.go:66: the traces:",
		"        === job Scan: image (failed) ===",
		"        2026-11-11T14:41:28.633311Z 01E Error: staged linux/amd64: trivy image exited with status 1: dependency unavailable",
		"=== RUN   TestTrial/defect",
		"    trial_test.go:66: the end of the failed job's trace:",
		printed,
		dialled,
		"        Error: artifact build-dist not found",
		"=== RUN   TestTrial/passedjob",
		"    trial_test.go:66: the traces:",
		"        === job Build: image (success) ===",
		unreached,
		"        === job Publish: release (failed) ===",
		"        2026-11-11T14:41:28.633311Z 01E Error: release v1 exists: validation failed",
		"=== RUN   TestTrial/onlypassedjob",
		"    trial_test.go:66: the traces:",
		"        === job Build: image (success) ===",
		unreached,
		"=== RUN   TestTrial/both",
		"    trial_test.go:66: the traces:",
		"        === job Build: image (failed) ===",
		unreached,
		"        === job Check: tests (failed) ===",
		"        Error: tests failed: validation failed",
		"=== RUN   TestTrial/harness",
		// The harness's own call to the forge is no job's trace.
		`    trial_test.go:70: livetest: Error: Get "https://forge.example/api/v1/version": dial tcp: lookup forge.example: i/o timeout: dependency unavailable`,
		"=== RUN   TestTrial/passed",
		"    trial_test.go:66: the end of a job's trace:",
		unreached,
		"--- FAIL: TestTrial (9.00s)",
		"    --- FAIL: TestTrial/unreached (3.00s)",
		"    --- FAIL: TestTrial/scan (3.00s)",
		"    --- FAIL: TestTrial/defect (2.00s)",
		"    --- FAIL: TestTrial/passedjob (2.00s)",
		"    --- FAIL: TestTrial/onlypassedjob (2.00s)",
		"    --- FAIL: TestTrial/both (2.00s)",
		"    --- FAIL: TestTrial/harness (2.00s)",
		"    --- PASS: TestTrial/passed (2.00s)",
		"FAIL",
		"FORGE_CI_LIVE_PACKAGE_END /src/test/live/conformance",
		"",
	}, "\n")

	verdict, err := ReadLiveRun(strings.NewReader(log), 1)
	if err != nil {
		t.Fatal(err)
	}

	if verdict.Verdict != liveVerdictFailed || verdict.ExitCode != 1 || len(verdict.Infrastructure) != 0 {
		t.Errorf("verdict %q, exit %d, infrastructure %v: a cause changes none of them", verdict.Verdict, verdict.ExitCode, verdict.Infrastructure)
	}

	want := map[string]string{
		"TestTrial/unreached":     "fail infrastructure: name resolution (github.com)",
		"TestTrial/scan":          "fail infrastructure: dependency unavailable",
		"TestTrial/defect":        "fail ",
		"TestTrial/passedjob":     "fail ",
		"TestTrial/onlypassedjob": "fail ",
		"TestTrial/both":          "fail ",
		"TestTrial/harness":       "fail ",
		"TestTrial/passed":        "pass ",
	}

	if len(verdict.Tests) != len(want) {
		t.Fatalf("tests %+v, want %d", verdict.Tests, len(want))
	}

	for _, test := range verdict.Tests {
		if got := test.Result + " " + test.Cause; got != want[test.Name] {
			t.Errorf("%s: %q, want %q", test.Name, got, want[test.Name])
		}
	}
}
