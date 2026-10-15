// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareReleaseImportsOnlyRegularRenderedData(t *testing.T) {
	t.Parallel()

	script := workflowStep(t, "prepare-release.yml", "prepare", "Download rendered release data").Run
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "data only", true: "linked data refused"}[linked], func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "prepare-release")
			box.write(".forge-ci-helpers/.forgejo/bin/lib-forge-ci.sh", `run_forge_ci() {
  cp -a -- "${FIXTURE:?}/." "${6:?}/"
}
`, 0o600)
			fixture := filepath.Join(box.root, "artifact")
			box.write("artifact/CHANGELOG.md", "rendered changelog\n", 0o600)
			box.write("artifact/commit-msg.txt", "chore(release): bump to v1.2.3\n", 0o600)
			box.write("artifact/.git/config", "renderer-controlled git config", 0o600)
			box.write("artifact/.git/hooks/pre-commit", "exit 99\n", 0o700)
			box.write("artifact/.forge-ci-helpers/injected", "renderer-controlled helper", 0o600)
			canary := box.write("canary", "untouched", 0o600)
			require.NoError(t, os.Symlink(canary, filepath.Join(box.root, "CHANGELOG.md")))

			if linked {
				require.NoError(t, os.Remove(filepath.Join(fixture, "CHANGELOG.md")))
				require.NoError(t, os.Symlink(canary, filepath.Join(fixture, "CHANGELOG.md")))
			}

			if out, err := git(t, box.root, "init", "--quiet", "--initial-branch=request"); err != nil {
				t.Fatalf("git init: %v: %s", err, out)
			}

			got := box.run(script, "RUNNER_TEMP="+t.TempDir(), "FIXTURE="+fixture, "TARGET_BRANCH=main")
			if (got.status == 0) == linked {
				t.Fatalf("linked=%t status=%d stderr=%s", linked, got.status, got.stderr)
			}

			if readSandboxFile(t, box, "canary") != "untouched" {
				t.Error("import followed the consumer's changelog symlink")
			}

			if !linked && readSandboxFile(t, box, "CHANGELOG.md") != "rendered changelog\n" {
				t.Error("rendered changelog was not imported")
			}

			for _, forbidden := range []string{".git/hooks/pre-commit", ".forge-ci-helpers/injected"} {
				if _, err := os.Lstat(filepath.Join(box.root, forbidden)); !os.IsNotExist(err) {
					t.Errorf("artifact path %s reached the signing checkout: %v", forbidden, err)
				}
			}

			if readSandboxFile(t, box, ".git/config") == "renderer-controlled git config" {
				t.Error("artifact replaced the signing checkout's Git config")
			}
		})
	}
}
