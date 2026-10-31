// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"io"
	"strings"
	"testing"
)

func TestClassifyLiveRunRequiresAPassingLeaf(t *testing.T) {
	for _, tc := range []struct {
		name, log, verdict string
		status, want       int
	}{
		{"leaf", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/public\n    --- PASS: TestTrial/public (1s)\n--- PASS: TestTrial (1s)"), "passed", 0, 0},
		{"entrypoint leaf", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (1s)"), "passed", 0, 0},
		{"skipped children", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/public\n--- SKIP: TestTrial/public (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"offline pass and live skip", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestOffline\n--- PASS: TestOffline (0s)\n--- SKIP: TestTrial (0s)"), "skipped:", 0, 1},
		{"offline pass and skipped children", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/public\n=== RUN   TestOffline\n--- PASS: TestOffline (0s)\n--- SKIP: TestTrial/public (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"unregistered", livePackageLog("one", "=== RUN   TestOffline\n--- PASS: TestOffline (0s)"), "skipped:", 0, 1},
		{"unbounded", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)", "skipped:", 0, 1},
		{"same name another package", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- SKIP: TestTrial (0s)") + livePackageLog("two", "=== RUN   TestTrial\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"same package another invocation", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- SKIP: TestTrial (0s)") + livePackageLog("one", "=== RUN   TestTrial\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"other package descendants", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)") + livePackageLog("two", "=== RUN   TestTrial/child\n--- SKIP: TestTrial/child (0s)"), "passed", 0, 0},
		{"name prefix is not ancestry", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrialOffline\n--- SKIP: TestTrial (0s)\n--- PASS: TestTrialOffline (0s)"), "skipped:", 0, 1},
		{"quoted PASS and skipped entrypoint", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n    trial_test.go:10: captured output:\n        --- PASS: TestTrial/reported-child (0.00s)\n--- SKIP: TestTrial (0.00s)\nPASS"), "skipped:", 0, 1},
		{"missing RUN", livePackageLog("one", "FORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"missing entrypoint RUN", livePackageLog("one", "FORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/child\n--- PASS: TestTrial/child (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"missing child RUN", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial/child (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"quoted RUN is not a RUN", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n        === RUN   TestTrial/child\n        --- PASS: TestTrial/child (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"missing entrypoint result", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/child\n--- PASS: TestTrial/child (0s)"), "skipped:", 0, 1},
		{"skipped entrypoint with passing child", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/child\n--- PASS: TestTrial/child (0s)\n--- SKIP: TestTrial (0s)"), "skipped:", 0, 1},
		{"missing actual ancestor result", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/group\n=== RUN   TestTrial/group/child\n--- PASS: TestTrial/group/child (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"skipped actual ancestor", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/group\n=== RUN   TestTrial/group/child\n--- PASS: TestTrial/group/child (0s)\n--- SKIP: TestTrial/group (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"literal slash is not an invented ancestor", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/sha1/application\n    --- PASS: TestTrial/sha1/application (0s)\n--- PASS: TestTrial (0s)"), "passed", 0, 0},
		{"successful actual ancestors", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/group\n=== RUN   TestTrial/group/child\n--- PASS: TestTrial/group/child (0s)\n--- PASS: TestTrial/group (0s)\n--- PASS: TestTrial (0s)"), "passed", 0, 0},
		{"RUN without child result prevents parent leaf", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/child\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"duplicate terminal result", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- SKIP: TestTrial (0s)\n--- PASS: TestTrial (0s)"), "failed", 0, 1},
		{"duplicate RUN", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial\n--- PASS: TestTrial (0s)"), "failed", 0, 1},
		{"nested boundaries", "FORGE_CI_LIVE_PACKAGE_BEGIN one\n" + livePackageLog("two", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)") + "FORGE_CI_LIVE_PACKAGE_END one\n", "failed", 0, 1},
		{"orphan end after valid pass", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)") + "FORGE_CI_LIVE_PACKAGE_END one\n", "failed", 0, 1},
		{"unclosed package", "FORGE_CI_LIVE_PACKAGE_BEGIN one\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)", "failed", 0, 1},
		{"wrong end", "FORGE_CI_LIVE_PACKAGE_BEGIN one\nFORGE_CI_LIVE_PACKAGE_END two\n", "failed", 0, 1},
		{"package only", "PASS\nok example/package", "skipped:", 0, 1},
		{"failed despite zero status", "--- PASS: TestOne (0s)\n--- FAIL: TestTwo (0s)", "failed", 0, 1},
		{"suite failure", "--- PASS: TestOne (0s)", "failed", 7, 7},
		{"interrupted", "--- PASS: TestOne (0s)", "interrupted", 130, 130},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ClassifyLiveRun(strings.NewReader(tc.log), tc.status)
			if err != nil || got.ExitCode != tc.want || !strings.HasPrefix(got.Verdict, tc.verdict) {
				t.Fatalf("classification: %+v, %v", got, err)
			}
		})
	}
	if got, err := ClassifyLiveRun(failedLiveLog{}, 0); err == nil || got.ExitCode == 0 {
		t.Fatalf("unreadable evidence was accepted: %+v, %v", got, err)
	}
}

func livePackageLog(name, body string) string {
	return livePackageBegin + name + "\n" + body + "\n" + livePackageEnd + name + "\n"
}

type failedLiveLog struct{}

func (failedLiveLog) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
