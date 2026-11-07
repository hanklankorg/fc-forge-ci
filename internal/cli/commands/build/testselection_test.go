// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestReleaseCLIRejectsInvalidSelectionsBeforeNativeExecution(t *testing.T) {
	for _, tc := range []struct{ ecosystem, selection string }{
		{"go", `{"packages":["./..."],"gate":"policy","run":null}`},
		{"go", `{"packages":["./..."]}`},
		{"go", `{"packages":["./..."],"gate":"optional"}`},
		{"cargo", `{"runs":[{"kind":"package-test","package":"app","name":"guard","gate":"policy"}],"runſ":[{"kind":"package-bin","package":"app","name":"main","gate":"application"}]}`},
	} {
		t.Run(tc.ecosystem+tc.selection, func(t *testing.T) {
			var out, stderr bytes.Buffer

			cmd := cli.New(cli.BuildInfo{})
			cmd.Writer, cmd.ErrWriter = &out, &stderr

			err := cmd.Run(t.Context(), []string{"forge-ci", "--provider", "local", "--runner", "local", "build", tc.ecosystem, "run", "--working-dir", t.TempDir(), "--test-selection-json", tc.selection})
			if !errors.Is(err, errs.ErrUsage) || out.Len() != 0 {
				t.Fatalf("selection not refused before build: %v %s", err, &out)
			}
		})
	}
}

func TestSelectedTestCLIRequiresExplicitSelection(t *testing.T) {
	for _, ecosystem := range []string{"go", "cargo"} {
		dir := t.TempDir()

		marker := filepath.Join(dir, "invoked")
		if err := os.WriteFile(filepath.Join(dir, ecosystem), []byte("#!/bin/sh\n: >\"${SELECTED_TEST_MARKER:?}\"\nexit 42\n"), 0o700); err != nil {
			t.Fatal(err)
		}

		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("SELECTED_TEST_MARKER", marker)

		for _, selection := range []string{"", " ", "{}", "null", `{"runs":[]}`} {
			var out, stderr bytes.Buffer

			cmd := cli.New(cli.BuildInfo{})
			cmd.Writer, cmd.ErrWriter = &out, &stderr

			err := cmd.Run(t.Context(), []string{"forge-ci", "--provider", "local", "--runner", "local", "build", ecosystem, "test", "--working-dir", t.TempDir(), "--test-selection-json", selection})
			if !errors.Is(err, errs.ErrUsage) || out.Len() != 0 {
				t.Fatalf("%s selection %q: got %v, output %s", ecosystem, selection, err, &out)
			}
		}

		selection := `{"packages":["./selected"],"gate":"policy"}`
		if ecosystem == "cargo" {
			selection = `{"runs":[{"kind":"package-test","package":"app","name":"guard","gate":"policy"}]}`
		}

		for _, tail := range [][]string{nil, {"--test-selection-json", selection, "unexpected-package"}} {
			var out, stderr bytes.Buffer

			cmd := cli.New(cli.BuildInfo{})
			cmd.Writer, cmd.ErrWriter = &out, &stderr

			args := append([]string{"forge-ci", "--provider", "local", "--runner", "local", "build", ecosystem, "test", "--working-dir", dir}, tail...)
			if err := cmd.Run(t.Context(), args); err == nil {
				t.Fatalf("%s accepted missing selection or positional argument: %v", ecosystem, tail)
			}
		}

		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("%s invoked a native tool for invalid CLI input: %v", ecosystem, err)
		}
	}
}

func TestSelectedTestCLIReusesNativePolicyGate(t *testing.T) {
	for _, ecosystem := range []string{"go", "cargo"} {
		for _, gate := range []string{"application", "policy"} {
			t.Run(ecosystem+"/"+gate, func(t *testing.T) {
				dir := t.TempDir()
				capture := filepath.Join(dir, "args")
				report := `{"Action":"start","Package":"example.test/app"}
{"Action":"run","Package":"example.test/app","Test":"TestExample"}
{"Action":"fail","Package":"example.test/app","Test":"TestExample"}
{"Action":"fail","Package":"example.test/app"}
`
				selection := `{"packages":["./selected"],"run":"^TestExample$","gate":"` + gate + `"}`
				terminal := "exit 1\n"

				if ecosystem == "cargo" {
					report = "running 1 test\ntest example ... FAILED\ntest result: FAILED. 0 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.00s\n"
					terminal = "printf '%s\\n' 'error: test failed, to rerun pass `-p app --test guard`' >&2\nexit 101\n"
					selection = `{"runs":[{"kind":"package-test","package":"app","name":"guard","gate":"` + gate + `"}]}`
				}

				program := "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"${SELECTED_TEST_ARGS:?}\"\ncat <<'NATIVE_REPORT'\n" + report + "NATIVE_REPORT\n" + terminal
				if err := os.WriteFile(filepath.Join(dir, ecosystem), []byte(program), 0o700); err != nil {
					t.Fatal(err)
				}

				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("SELECTED_TEST_ARGS", capture)

				var out, stderr bytes.Buffer

				cmd := cli.New(cli.BuildInfo{})
				cmd.Writer, cmd.ErrWriter = &out, &stderr

				commandArgs := []string{"forge-ci", "--provider", "local", "--runner", "local", "--enforce-application-tests=false", "build", ecosystem, "test", "--working-dir", dir, "--test-selection-json", selection}
				if ecosystem == "go" {
					commandArgs = append(commandArgs, "--build-tags", "unit")
				}

				err := cmd.Run(t.Context(), commandArgs)
				if (err != nil) != (gate == "policy") {
					t.Fatalf("%s %s gate: got %v; stdout=%s stderr=%s", ecosystem, gate, err, &out, &stderr)
				}

				args, readErr := os.ReadFile(capture)
				if readErr != nil {
					t.Fatal(readErr)
				}

				want := []string{"test", "-json", "-count=1", "-race", "-buildvcs=false", "-tags", "unit", "-run", "^TestExample$", "./selected"}
				if ecosystem == "cargo" {
					want = []string{"test", "--locked", "--package", "app", "--test", "guard"}
				}

				if string(args) != strings.Join(want, "\n")+"\n" {
					t.Fatalf("native argv: got %q, want %q", args, want)
				}

				if !strings.Contains(out.String(), report) {
					t.Fatalf("native stdout was not preserved: %s", &out)
				}

				if ecosystem == "cargo" && !strings.Contains(stderr.String(), "error: test failed,") || ecosystem == "go" && stderr.Len() != 0 {
					t.Fatalf("native stderr routing changed: %s", &stderr)
				}
			})
		}
	}
}
