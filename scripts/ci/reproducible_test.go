// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Only the comparison mode runs: no compiler, checkout, network or product.
func TestReleaseArchiveComparison(t *testing.T) {
	script, err := filepath.Abs("verify-reproducible.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"equal", "different", "missing", "missing-both", "wrong-target", "empty", "zero-byte"} {
		t.Run(scenario, func(t *testing.T) {
			first, second := filepath.Join(t.TempDir(), "first build"), filepath.Join(t.TempDir(), "second build")
			for _, dir := range []string{first, second} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"} {
					if scenario == "empty" || (target == "linux_amd64" && (scenario == "missing-both" || (scenario == "missing" && dir == second))) {
						continue
					}
					name, content := "forge-ci_1.2.3_"+target+".tar.gz", "same payload"
					if scenario == "zero-byte" {
						content = ""
					}
					if target == "linux_amd64" {
						if scenario == "different" && dir == second {
							content = "changed payload"
						}
						if scenario == "wrong-target" {
							name = "forge-ci_1.2.3_windows_amd64.tar.gz"
						}
					}
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			output, err := exec.CommandContext(t.Context(), "bash", script, "--compare", first, second).CombinedOutput()
			if scenario == "equal" {
				if err != nil || !strings.Contains(string(output), "4 release files are byte-identical") {
					t.Fatalf("comparison failed: %v\n%s", err, output)
				}
			} else if err == nil || !(strings.Contains(string(output), "Expected") || strings.Contains(string(output), "Different or missing")) {
				t.Fatalf("expected an actionable comparison failure: %v\n%s", err, output)
			}
		})
	}
}
