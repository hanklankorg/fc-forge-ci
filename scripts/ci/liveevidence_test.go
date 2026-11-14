// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"path/filepath"
	"testing"
)

func TestRunLiveTests_JobEvidenceDestination(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"default", "explicit", "legacy", "preferred"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			override := filepath.Join(t.TempDir(), "job-evidence")

			var env []string

			switch name {
			case "explicit":
				env = []string{"FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR=" + override}
			case "legacy":
				env = []string{"FORGE_CI_LIVE_SNAPSHOT_EVIDENCE=" + override}
			case "preferred":
				env = []string{"FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR=" + override, "FORGE_CI_LIVE_SNAPSHOT_EVIDENCE=/unused"}
			}

			run := newLiveRun(t, env...)

			out, errOut, status := run.exec(t, "focused", "SnapshotTrial")
			if status != 0 {
				t.Fatalf("status %d: %s\n%s", status, out, errOut)
			}

			log, _ := run.logFile(t)

			want := override
			if name == "default" {
				want = filepath.Join(filepath.Dir(log), "job-logs")
			}

			if got := run.goTestEnv(t)["FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR"]; got != want {
				t.Fatalf("suite evidence directory = %q, want %q", got, want)
			}
		})
	}
}
