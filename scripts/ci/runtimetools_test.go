// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Only this inert payload translates the received fixed image PATH into an
// owned filesystem. Production helper/verification bytes are not rewritten.
const runtimeToolLookupFixture = `
[[ "$PATH" == /usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/bin:/bin ]]
[[ "$RUSTUP_HOME" == /usr/local/rustup && "$CARGO_HOME" == "$HOME/.cargo" ]]
IFS=: read -r -a directories <<<"$PATH"
for tool in gh go cargo rustc cosign git-cliff; do
  found=false
  for directory in "${directories[@]}"; do
    candidate="$TEST_IMAGE_ROOT$directory/$tool"
    if [[ -x "$candidate" ]]; then "$candidate"; found=true; break; fi
  done
  [[ "$found" == true ]]
done
`

// The finite profiles and the exact directories each one admits. The host
// profiles never export the image's Cargo homes: a hosted runner has none.
// $HOME stands for the job's home directory.
var runtimeToolProfiles = map[string]string{
	"runtime":        "/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/bin:/bin",
	"host":           "/usr/bin:/bin",
	"host-toolchain": "/usr/bin:/bin:/usr/local/bin:$HOME/.cargo/bin",
}

// The profile assumes a hosted runner has cargo in the runner user's Cargo
// directory. The key-free version-file job must find it there, or a Cargo bump cannot
// refresh Cargo.lock; the jobs that hold keys must not.
func TestHostToolchainFindsRunnerCargo(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cargo/bin"), 0o700); err != nil {
		t.Fatal(err)
	}

	canonicalWrite(t, filepath.Join(home, ".cargo/bin/cargo"), "#!/bin/bash\nprintf 'runner cargo %s\\n' \"$*\"\n", 0o700)

	run := func(profile string) (string, error) {
		cmd := exec.CommandContext(t.Context(), "/bin/bash", "-p", "trusted-runtime-tools.sh", profile, "cargo", "update")
		cmd.Env = []string{"HOME=" + home, "PATH=/consumer/hostile"}

		out, err := cmd.CombinedOutput()

		return string(out), err
	}

	if out, err := run("host-toolchain"); err != nil || out != "runner cargo update\n" {
		t.Fatalf("host-toolchain did not run the runner's cargo: %v, %s", err, out)
	}

	if out, err := run("host"); err == nil {
		t.Fatalf("host ran a tool of the home directory: %s", out)
	}
}

func TestRuntimeToolMetadataLookup(t *testing.T) {
	helper, err := filepath.Abs("trusted-runtime-tools.sh")
	if err != nil {
		t.Fatal(err)
	}

	for profile, path := range runtimeToolProfiles {
		t.Run(profile, func(t *testing.T) {
			root := t.TempDir()
			path := strings.ReplaceAll(path, "$HOME", root)

			// gh lives where the hosted runner and the image both place a
			// root-owned system tool; the profiles differ in what else they admit.
			imageTool := filepath.Join(root, "image/usr/bin/gh")
			if err := os.MkdirAll(filepath.Dir(imageTool), 0o700); err != nil {
				t.Fatal(err)
			}

			canonicalWrite(t, imageTool, "#!/bin/bash\nprintf '%s\\n' \"$*\"\nprintf '%s|%s|%s\\n' \"$PATH\" \"${RUSTUP_HOME-unset}\" \"${CARGO_HOME-unset}\" >\"$TOOL_ENVIRONMENT\"\n", 0o700)
			// Mock only exec's filesystem boundary to represent immutable system
			// directories without modifying the host. Run the actual helper's
			// assignments/refusal.
			const execute = `
exec() {
  IFS=: read -r -a directories <<<"$PATH"
  for directory in "${directories[@]}"; do
    if [[ -x "$TEST_IMAGE_ROOT$directory/$1" ]]; then
      local tool="$TEST_IMAGE_ROOT$directory/$1"
      shift
      "$tool" "$@"
      return
    fi
  done
  return 69
}
source "$1" "$2" gh api --hostname approved.invalid repos/owner/product/actions/artifacts/123
`

			cmd := exec.CommandContext(t.Context(), "/bin/bash", "--noprofile", "--norc", "-peu", "-o", "pipefail", "-c", execute, "metadata", helper, profile)
			cmd.Env = []string{"HOME=" + root, "PATH=/consumer/hostile", "TEST_IMAGE_ROOT=" + filepath.Join(root, "image"), "TRUSTED_TOOL_PATH=/consumer/hostile",
				"TOOL_ENVIRONMENT=" + filepath.Join(root, "environment"), "RUSTUP_HOME=/consumer/toolchain", "CARGO_HOME=/consumer/cache"}

			out, runErr := cmd.CombinedOutput()
			if runErr != nil || string(out) != "api --hostname approved.invalid repos/owner/product/actions/artifacts/123\n" {
				t.Fatalf("metadata tool lookup: %v, %s", runErr, out)
			}

			want := path + "|/usr/local/rustup|" + root + "/.cargo\n"
			if profile != "runtime" {
				// Inherited Cargo homes are left as they are: the host profiles
				// neither trust nor rewrite them; the runner's cargo reads its own.
				want = path + "|/consumer/toolchain|/consumer/cache\n"
			}

			environment, err := os.ReadFile(filepath.Join(root, "environment"))
			if err != nil || string(environment) != want {
				t.Fatalf("profile %s environment %q (%v), want %q", profile, environment, err, want)
			}
		})
	}
}

func TestRuntimeToolHelperRefusesUnknownProfileAndImplicitCLI(t *testing.T) {
	for _, scenario := range []struct {
		name, want, home string
		args             []string
	}{
		{name: "no-arguments", want: "expected PROFILE COMMAND"},
		{name: "profile-only", args: []string{"host"}, want: "expected PROFILE COMMAND"},
		{name: "unknown-profile", args: []string{"hosted", "gh", "api"}, want: "unknown tool profile: hosted"},
		{name: "consumer-path-profile", args: []string{"/consumer/hostile", "gh"}, want: "unknown tool profile"},
		{name: "implicit-cli-runtime", args: []string{"runtime", "forge-ci", "--version"}, want: "forge-ci must be verified and absolute"},
		{name: "implicit-cli-host", args: []string{"host", "forge-ci", "--version"}, want: "forge-ci must be verified and absolute"},
		{name: "relative-home", home: "consumer/home", args: []string{"host-toolchain", "cargo"}, want: "HOME must be an absolute path without a colon"},
		{name: "home-of-two-directories", home: "/home/runner:/consumer/hostile", args: []string{"host-toolchain", "cargo"}, want: "HOME must be an absolute path without a colon"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			home := scenario.home
			if home == "" {
				home = t.TempDir()
			}

			cmd := exec.CommandContext(t.Context(), "/bin/bash", append([]string{"-p", "trusted-runtime-tools.sh"}, scenario.args...)...)
			cmd.Env = []string{"HOME=" + home, "PATH=/consumer/hostile"}

			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), scenario.want) {
				t.Fatalf("accepted or wrong refusal: %v: %s", err, out)
			}
		})
	}
}

func TestCanonicalDeliveryRejectsUnadmittedToolHelper(t *testing.T) {
	fixture := newCanonicalFixture(t)
	canonicalWrite(t, filepath.Join(fixture.engine, "scripts/ci/trusted-runtime-tools.sh"), "printf forbidden >>\"$PROCESS_MARKER\"\n", 0o600)

	if out, err := fixture.run(t, "prepare"); err == nil {
		t.Fatalf("modified tool helper passed before metadata: %s", out)
	}

	if _, err := os.Stat(fixture.marker); !os.IsNotExist(err) {
		t.Fatal("modified helper or CLI ran")
	}
}
