// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package genericcli_test

import (
	"os"
	"os/exec"
	"testing"
)

func fixtureTool(t *testing.T, name string) string {
	t.Helper()
	if path := os.Getenv("FORGE_CI_TEST_" + name); path != "" {
		return path
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is required for overlay quality preflight", name)
	}
	return path
}

func TestAdapterShellFormat(t *testing.T) {
	tool := fixtureTool(t, "shfmt")
	for _, file := range []string{"prepare.sh", "check-release.sh", "build-release.sh", "verify-rust-job.sh"} {
		if output, err := exec.CommandContext(t.Context(), tool, "-d", file).CombinedOutput(); err != nil {
			t.Fatalf("shfmt %s: %v\n%s", file, err, output)
		}
	}
}

// Syntax-check only: never execute a preparer, compiler, product or live suite.
func TestAdapterShellSyntax(t *testing.T) {
	for _, file := range []string{"prepare.sh", "check-release.sh", "build-release.sh", "verify-rust-job.sh"} {
		t.Run(file, func(t *testing.T) {
			if output, err := exec.CommandContext(t.Context(), "bash", "-n", file).CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, output)
			}
		})
	}
}
