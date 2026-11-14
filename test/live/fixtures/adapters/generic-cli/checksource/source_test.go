// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

type sourceCheckError int

func (e sourceCheckError) Error() string { return fmt.Sprintf("inert exit %d", e) }
func (e sourceCheckError) ExitCode() int { return int(e) }

func TestGenericCLIOriginalGates(t *testing.T) {
	for _, tc := range []struct {
		name, fail string
		status     int
		wantError  bool
		wantTests  bool
	}{
		{name: "clean", wantTests: true},
		{name: "diagnostic findings", fail: "nanolinter verify", status: 1, wantTests: true},
		{name: "cross-platform lint", fail: "just _lint-release-platforms", status: 1, wantError: true},
		{name: "format", fail: "just --fmt --check", status: 1, wantError: true},
		{name: "tidy", fail: "go mod tidy -diff", status: 1, wantError: true},
		{name: "tool readiness", fail: "nanolinter check-tools", status: 1, wantError: true},
		{name: "missing command", fail: "nanolinter verify", status: 127, wantError: true},
		{name: "invalid invocation", fail: "nanolinter verify", status: 78, wantError: true},
		{name: "cancelled", fail: "nanolinter verify", status: 130, wantError: true},
		{name: "application tests", fail: "just test", status: 1, wantError: true, wantTests: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string

			err := genericCLISteps(func(name string, args ...string) error {
				call := strings.Join(append([]string{name}, args...), " ")
				calls = append(calls, call)

				if call == tc.fail {
					return sourceCheckError(tc.status)
				}

				return nil
			})
			if (err != nil) != tc.wantError || slices.Contains(calls, "just test") != tc.wantTests {
				t.Fatalf("result=%v calls=%v", err, calls)
			}
		})
	}

	t.Run("execution failure is not a diagnostic", func(t *testing.T) {
		failure := errors.New("inert execution failure")

		err := genericCLISteps(func(string, ...string) error { return failure })
		if !errors.Is(err, failure) {
			t.Fatalf("lost blocking execution error: %v", err)
		}
	})
}

func TestGenericCLIOriginalTreeGate(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		t.Run(fmt.Sprintf("tampered=%t", tampered), func(t *testing.T) {
			testenv.New(t)
			root := t.TempDir()
			t.Chdir(root)
			source := filepath.Join(root, ".gitlab/generic-cli-original-source")
			workflow := filepath.Join(source, ".forgejo/workflows/prepare-release.yml")

			if err := os.MkdirAll(filepath.Dir(workflow), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(workflow, []byte("original workflow\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			git := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", source}, args...)...)

				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git: %v\n%s", err, out)
				}

				return strings.TrimSpace(string(out))
			}
			git("init", "--quiet", "--object-format=sha1")
			git("add", "--all")
			tree := git("write-tree")

			if err := os.RemoveAll(filepath.Join(source, ".git")); err != nil {
				t.Fatal(err)
			}

			metadata, err := json.Marshal(map[string]any{
				"commit": strings.Repeat("a", 40), "tree": tree, "objectFormat": "sha1",
				"lintPolicy": "informational", "excludedChecks": []string{},
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(root, ".gitlab/generic-cli-source.json"), metadata, 0o600); err != nil {
				t.Fatal(err)
			}

			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}

			calls := filepath.Join(root, "calls")

			for _, name := range []string{"just", "go", "nanolinter"} {
				body := "#!/bin/sh\nset -eu\ntest -f .forgejo/workflows/prepare-release.yml\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >>\"$CHECK_CALLS\"\n"
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CHECK_CALLS", calls)

			if tampered {
				if err := os.WriteFile(workflow, []byte("altered workflow\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			err = genericCLIChecks(t.Context())
			if (err != nil) != tampered {
				t.Fatalf("original tree gate: %v", err)
			}

			body, readErr := os.ReadFile(calls)
			if tampered {
				if !os.IsNotExist(readErr) {
					t.Fatal("a modified original source reached tool execution")
				}
			} else if readErr != nil || !strings.Contains(string(body), "just test\n") {
				t.Fatalf("original application checks were not run: %s %v", body, readErr)
			}

			if _, err := os.Lstat(filepath.Join(source, ".git")); !os.IsNotExist(err) {
				t.Fatal("private Git state was not cleaned")
			}
		})
	}
}
