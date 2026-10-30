// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package genericcli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintDiagnosticAndApplicationGates(t *testing.T) {
	adapter, err := filepath.Abs("check-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"cargo", "go"} {
		for _, tc := range []struct {
			name, fail, lint string
			wantExit         int
			tests            bool
		}{
			{"clean", "", "0", 0, true}, {"all-lint-findings-informational", "", "1", 0, true},
			{"tests-still-block", "test", "1", 23, true}, {"setup-still-blocks", "setup", "1", 23, false},
			{"missing-tools-block", "tools", "1", 23, false}, {"format-blocks", "--fmt", "1", 23, false},
			{"invocation-error-blocks", "", "78", 78, false}, {"missing-command-blocks", "", "127", 127, false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				for _, dir := range []string{bin, filepath.Join(root, ".gitlab"), filepath.Join(root, "target/release")} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				write := func(path, body string) {
					t.Helper()
					if err := os.WriteFile(path, []byte(body), 0700); err != nil {
						t.Fatal(err)
					}
				}
				write(filepath.Join(root, ".gitlab/generic-cli-kind"), kind+"\n")
				write(filepath.Join(root, ".gitlab/generic-cli-source.json"), `{"lintPolicy":"informational","excludedChecks":[]}`)
				write(filepath.Join(bin, "rustc"), "#!/bin/sh\nprintf 'rustc 1.99.1 (fixture)\\n'\n")
				write(filepath.Join(bin, "go"), "#!/bin/sh\nif [ \"$1\" = env ]; then printf 'go1.27.3\\n'; else printf 'go %s\\n' \"$*\" >> \"$TEST_CALLS\"; fi\n")
				write(filepath.Join(bin, "just"), "#!/bin/sh\nprintf 'just %s\\n' \"$*\" >> \"$TEST_CALLS\"\ncase \"$1:$FAIL_GROUP\" in _build-verify-binary:setup|_lint-linux-platform:setup|--fmt:--fmt|test:test) exit 23;; esac\nif [ \"$1\" = _build-verify-binary ] && [ \"$FAIL_GROUP\" = tools ]; then exit 23; fi\n")
				nano := "#!/bin/sh\nprintf 'nano %s\\n' \"$*\" >> \"$TEST_CALLS\"\nif [ \"$1\" = check-tools ]; then if [ \"$FAIL_GROUP\" = tools ]; then exit 23; fi; exit 0; fi\nprintf 'SUMMARY: KICS 64 findings; other checks executed\\n'\nexit \"$LINT_EXIT\"\n"
				write(filepath.Join(bin, "nanolinter"), nano)
				write(filepath.Join(root, "target/release/nanolinter"), nano)
				calls := filepath.Join(root, "calls")
				cmd := exec.CommandContext(t.Context(), "bash", adapter)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TEST_CALLS="+calls, "FAIL_GROUP="+tc.fail, "LINT_EXIT="+tc.lint)
				out, err := cmd.CombinedOutput()
				exit := 0
				if err != nil {
					if e, ok := err.(*exec.ExitError); ok {
						exit = e.ExitCode()
					} else {
						t.Fatal(err)
					}
				}
				if exit != tc.wantExit {
					t.Fatalf("exit %d want %d: %s", exit, tc.wantExit, out)
				}
				body, _ := os.ReadFile(calls)
				log := string(body)
				if strings.Contains(log, "just test\n") != tc.tests {
					t.Fatalf("test group coverage: %s", log)
				}
				if strings.Contains(log, "--disable") || strings.Contains(log, "--warn") || strings.Contains(log, "just verify") {
					t.Fatalf("suppressed checks/blanket gate: %s", log)
				}
				if tc.tests && (!strings.Contains(log, "nano verify\n") || !strings.Contains(string(out), "NANOLINTER_DIAGNOSTIC exit="+tc.lint) || !strings.Contains(string(out), "SUMMARY: KICS 64 findings")) {
					t.Fatalf("lost full diagnostic invocation/evidence: %s\n%s", log, out)
				}
			})
		}
	}
}
