// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Run the real process entry point, including its argument-count dispatch and
// exits, without starting a compiler, engine or provider inside the fixture.
func TestPreflightCLIProcess(_ *testing.T) {
	if os.Getenv("FORGE_CI_TEST_PREFLIGHT_PROCESS") != "1" {
		return
	}

	for index, argument := range os.Args {
		if argument == "--" {
			os.Args = append([]string{"preflight"}, os.Args[index+1:]...)

			main()
			os.Exit(0)
		}
	}

	os.Exit(99)
}

func preflightCLIEnvironment(t *testing.T) []string {
	t.Helper()

	env := []string{}

	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "FORGE_CI_IMAGE_BUILD_") {
			env = append(env, value)
		}
	}

	return append(env, "FORGE_CI_TEST_PREFLIGHT_PROCESS=1")
}

func TestPreflightCLIZeroResourceOptions(t *testing.T) {
	for _, required := range []bool{false, true} {
		name, want := "default", "default\n\n\n\n"
		env := preflightCLIEnvironment(t)

		if required {
			name = "inherited-required"

			env = append(env, "FORGE_CI_IMAGE_BUILD_BUDGET=required", "FORGE_CI_IMAGE_BUILD_CPUS=3", "FORGE_CI_IMAGE_BUILD_MEMORY_MIB=2048", "FORGE_CI_IMAGE_BUILD_COMPILER_JOBS=1")
			want = "required\n3\n2048\n1\n--cpu-period\n100000\n--cpu-quota\n300000\n--memory\n2147483648\n--memory-swap\n2147483648\n--jobs\n1\n--retry\n0\n"
		}

		t.Run(name, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}

			cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestPreflightCLIProcess$", "--", "image-build-settings-options")
			cmd.Env = env

			output, err := cmd.CombinedOutput()
			if err != nil || string(output) != want {
				t.Fatalf("real CLI: %v, got %q, want %q", err, output, want)
			}
		})
	}
}

func TestPreflightCLIPrepareLiveWithoutResourceWords(t *testing.T) {
	for _, mode := range []string{"default", "inherited-required", "invalid-inherited", "conflicting-explicit"} {
		t.Run(mode, func(t *testing.T) {
			root, bin := t.TempDir(), t.TempDir()

			write := func(name, body string) {
				t.Helper()

				if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(name, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"scripts/ci/prepare-live.sh", "scripts/ci/image-build-settings.sh"} {
				body, err := os.ReadFile(filepath.Join(reporoot.Path(t), name))
				if err != nil {
					t.Fatal(err)
				}

				write(filepath.Join(root, name), string(body))
			}
			// The Go executable is only transport to the real CLI entry point above;
			// no settings, numeric policy or refusal is synthesized by this shim.
			write(filepath.Join(bin, "go"), "#!/usr/bin/env bash\n[[ $1 == run && $2 == ./test/live/internal/livetest/preflight ]] || exit 99\nshift 2\nexec \"$PREFLIGHT_TEST_BINARY\" '-test.run=^TestPreflightCLIProcess$' -- \"$@\"\n")
			write(filepath.Join(bin, "git"), "#!/usr/bin/env bash\ncase \"$*\" in *status*) exit 0 ;; *) echo fixture ;; esac\n")
			write(filepath.Join(root, "scripts/ci/engine-binary.sh"), `#!/usr/bin/env bash
printf '%s\0' "$@" >"$RECEIPT.argv"
printf '%s\0' "${FORGE_CI_IMAGE_BUILD_BUDGET-unset}" "${FORGE_CI_IMAGE_BUILD_CPUS-unset}" "${FORGE_CI_IMAGE_BUILD_MEMORY_MIB-unset}" "${FORGE_CI_IMAGE_BUILD_COMPILER_JOBS-unset}" "${GOMAXPROCS-unset}" "${GOMEMLIMIT-unset}" >"$RECEIPT.env"
exit 17
`)

			lab, destination := t.TempDir(), t.TempDir()
			write(filepath.Join(lab, "justfile"), "")
			sentinel := filepath.Join(destination, "prepared.env")
			write(sentinel, "preserve\n")

			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}

			args := []string{filepath.Join(root, "scripts/ci/prepare-live.sh"), lab, destination, "forgejo"}
			env := append(preflightCLIEnvironment(t), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GO="+filepath.Join(bin, "go"), "PREFLIGHT_TEST_BINARY="+binary, "RECEIPT="+filepath.Join(root, "receipt"))
			wantStatus := 17

			if mode != "default" {
				env = append(env, "FORGE_CI_IMAGE_BUILD_BUDGET=required", "FORGE_CI_IMAGE_BUILD_CPUS=3", "FORGE_CI_IMAGE_BUILD_MEMORY_MIB=2048", "FORGE_CI_IMAGE_BUILD_COMPILER_JOBS=1")
			}

			if mode == "invalid-inherited" {
				env = append(env, "FORGE_CI_IMAGE_BUILD_CPUS=03")
				wantStatus = 2
			}

			if mode == "conflicting-explicit" {
				args = append(args, "build-budget=default")
				wantStatus = 2
			}

			cmd := exec.CommandContext(t.Context(), "bash", args...)
			cmd.Env, cmd.Dir = env, root
			output, err := cmd.CombinedOutput()

			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != wantStatus {
				t.Fatalf("exit: %v: %s", err, output)
			}

			argv, err := os.ReadFile(filepath.Join(root, "receipt.argv"))
			if wantStatus == 2 {
				if !os.IsNotExist(err) {
					t.Fatalf("refusal reached owner effect: %q, %v", argv, err)
				}
			} else {
				if err != nil || string(argv) != "check\x00" {
					t.Fatalf("next owner: %q, %v", argv, err)
				}

				settings, err := os.ReadFile(filepath.Join(root, "receipt.env"))
				if err != nil {
					t.Fatal(err)
				}

				if mode == "inherited-required" && string(settings) != "required\x003\x002048\x001\x001\x002048MiB\x00" {
					t.Fatalf("inherited settings: %q", settings)
				}

				if mode == "default" && !strings.HasPrefix(string(settings), "default\x00unset\x00unset\x00unset\x00") {
					t.Fatalf("legacy settings: %q", settings)
				}
			}

			body, err := os.ReadFile(sentinel)
			if err != nil || string(body) != "preserve\n" {
				t.Fatal("destination changed before successful canonical check")
			}
		})
	}
}
