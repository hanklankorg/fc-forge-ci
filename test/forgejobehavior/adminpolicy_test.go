// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// A consumer must not select a different embedded policy by replacing the
// executable through the old development seam or setting SIGNER_STRICT=0.
func TestAdminPolicyActionsRejectConsumerEngineReplacement(t *testing.T) {
	cases := []struct {
		action, step string
		env          []string
	}{
		{"build-go", "Build Go module", goBuildInputs("abc")},
		{"build-cargo", "Build Cargo project", nil},
		{"build-maven", "Build Maven project", mavenInputs()},
		{"lint-nanolinter", "Run nanolinter verify", []string{"LINT_OFFLINE=false", "LINT_REQUIRED_LINTS=secrets,sast", "LINT_DISABLE_CHECKS="}},
	}
	for _, test := range cases {
		t.Run(test.action, func(t *testing.T) {
			box := newSandbox(t, test.action)
			box.write("service/pom.xml", "<project/>\n", 0o600)
			box.write(".engine/bin/lib-forge-ci.sh", string(reporoot.ReadFile(t, ".engine/bin/lib-forge-ci.sh")), 0o600)
			for _, arch := range []string{"amd64", "arm64"} {
				file := ".engine/bin/forge-ci-linux-" + arch + ".sha256"
				box.write(file, string(reporoot.ReadFile(t, file)), 0o600)
			}
			marker := filepath.Join(box.root, "untrusted-engine-ran")
			rogue := box.write("tools/rogue-engine", "#!/bin/sh\nprintf ran > '"+marker+"'\nexit 0\n", 0o700)
			box.write("tools/nanolinter", "#!/bin/sh\nexit 0\n", 0o700)
			env := append([]string{"FORGE_CI_BIN=" + rogue, "SIGNER_STRICT=0", "PATH=" + filepath.Join(box.root, "tools") + ":" + os.Getenv("PATH")}, test.env...)
			got := box.run(actionStep(t, test.action, test.step), env...)
			if got.status == 0 || !strings.Contains(got.stderr, "does not match the recorded pin") {
				t.Fatalf("consumer engine replacement was not refused: status=%d %s", got.status, got.stderr)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("untrusted executable ran: %v", err)
			}
		})
	}
}
