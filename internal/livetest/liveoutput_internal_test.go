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
		{"leaf", "--- PASS: TestTrial/public (1s)\n--- PASS: TestTrial (1s)", "passed", 0, 0},
		{"skipped children", "--- SKIP: TestTrial/public (0s)\n--- PASS: TestTrial (0s)", "skipped:", 0, 1},
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

type failedLiveLog struct{}

func (failedLiveLog) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
