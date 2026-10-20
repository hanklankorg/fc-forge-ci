// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// setupToolchainFakeEngine records argv like fakeEngine and also its
// environment, next to the argv log.
const setupToolchainFakeEngine = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\0' "$@" >"${FAKE_ARGV_LOG:?}"
env >"${FAKE_ARGV_LOG:?}.env"
`

// Each engine-delegating step passes its inputs as separate argv values, and
// the mise GitHub token reaches the engine only as a presence flag.
func TestSetupToolchainEngineCalls(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		step string
		env  []string
		argv func(s *sandbox) []string
	}{
		{"Choose the Go provider", []string{"FORGEJO_PATH=/runner/path-file", "FORGEJO_ENV=/runner/env-file"}, func(*sandbox) []string {
			return []string{"toolchain", "setup-go-env", "--path-file", "/runner/path-file", "--env-file", "/runner/env-file"}
		}},
		{"Install bootstrap system dependencies", nil, func(*sandbox) []string {
			return []string{"toolchain", "install-system-dependencies", "--packages", "gettext libxml2-utils", "--skip-if-missing-apt"}
		}},
		{"Trust consumer mise config", nil, func(s *sandbox) []string {
			return []string{"toolchain", "trust-mise-config", "--mise-bin", filepath.Join(s.root, "home", ".local/bin/mise")}
		}},
		{"Validate mise install mode", []string{"MISE_LOCKED_INSTALL=true", "MISE_GITHUB_TOKEN=ghs-secret", "TOOLS_SUBSET=java maven aqua:aquasecurity/trivy"}, func(*sandbox) []string {
			return []string{"toolchain", "validate-mise-install", "--tools", "java maven aqua:aquasecurity/trivy", "--locked", "true", "--github-token-present=true"}
		}},
		{"Validate mise install mode", []string{"MISE_LOCKED_INSTALL=false", "MISE_GITHUB_TOKEN=", "TOOLS_SUBSET="}, func(*sandbox) []string {
			return []string{"toolchain", "validate-mise-install", "--tools", "", "--locked", "false", "--github-token-present=false"}
		}},
		{"Install tools (cache miss)", []string{"MISE_LOCKED_INSTALL=true", "TOOLS_SUBSET=aqua:a/b npm:c", "MISE_INSTALL_HTTP_TIMEOUT_SECONDS=240"}, func(s *sandbox) []string {
			return []string{"toolchain", "install-mise-tools", "--locked", "true", "--tools", "aqua:a/b npm:c",
				"--http-timeout-seconds", "240", "--mise-bin", filepath.Join(s.root, "home", ".local/bin/mise")}
		}},
		{"Expose installed tools", []string{"MISE_LOCKED_INSTALL=false", "FORGEJO_PATH=/runner/path-file"}, func(*sandbox) []string {
			return []string{"toolchain", "expose-mise-tools", "--path-file", "/runner/path-file", "--locked", "false"}
		}},
	} {
		t.Run(test.step+" "+strings.Join(test.env, " "), func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "setup-toolchain")
			// Record the engine environment too, so a token handed over by
			// environment instead of argv is caught.
			box.write(".forgejo/bin/forge-ci", setupToolchainFakeEngine, 0o700)

			got := box.run(loadAction(t, "setup-toolchain").step(t, test.step).Run, test.env...)
			if want := test.argv(box); got.status != 0 || !slices.Equal(box.argv(), want) {
				t.Fatalf("status %d argv %q, want %q; stderr %s", got.status, box.argv(), want, got.stderr)
			}

			env, err := os.ReadFile(box.argvLog + ".env")
			if err != nil || strings.Contains(string(env), "ghs-secret") {
				t.Fatalf("engine environment carries the mise GitHub token (%v):\n%s", err, env)
			}
		})
	}
}

// mise gives a connection or a pause between reads 30 seconds by default,
// which failed the Go download on a slow link. The install step hands the
// engine the action's own timeout, and through the engine of this tree every
// mise call of the install carries it as MISE_HTTP_TIMEOUT, whatever the job
// environment held.
func TestSetupToolchainInstallRaisesTheMiseHTTPTimeout(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "setup-toolchain")

	input := action.Inputs["mise-http-timeout-seconds"]
	if input.Required || input.Default != "120" {
		t.Fatalf("mise-http-timeout-seconds: required %v, default %v; want an optional input of 120", input.Required, input.Default)
	}

	install := action.step(t, "Install tools (cache miss)")
	if got := install.Env["MISE_INSTALL_HTTP_TIMEOUT_SECONDS"]; got != "${{ inputs.mise-http-timeout-seconds }}" {
		t.Fatalf("the install step takes its timeout from %q", got)
	}

	box, env := engineSandbox(t, "setup-toolchain", nil)
	box.write(".mise.toml", "[tools]\ngo = \"1.26.6\"\n\"aqua:a/b\" = \"1.0.0\"\n", 0o600)

	calls := filepath.Join(box.root, "mise-calls")
	box.write("home/.local/bin/mise", "#!/usr/bin/env bash\nprintf '%s %s\\n' \"${MISE_HTTP_TIMEOUT-unset}\" \"$*\" >>\""+calls+"\"\n", 0o700)

	got := box.run(install.Run, append(env,
		"MISE_LOCKED_INSTALL=false", "TOOLS_SUBSET=", "MISE_INSTALL_HTTP_TIMEOUT_SECONDS=240", "MISE_HTTP_TIMEOUT=30s")...)
	if got.status != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
	}

	recorded := strings.Split(strings.TrimSuffix(readSandboxFile(t, box, "mise-calls"), "\n"), "\n")
	if len(recorded) != 2 {
		t.Fatalf("mise ran %d times, want the Go runtime and the tool install: %q", len(recorded), recorded)
	}

	for _, call := range recorded {
		if !strings.HasPrefix(call, "240s ") || !strings.Contains(call, " install") {
			t.Errorf("mise call %q, want an install with MISE_HTTP_TIMEOUT=240s", call)
		}
	}

	refused := box.run(install.Run, append(env, "MISE_LOCKED_INSTALL=false", "TOOLS_SUBSET=", "MISE_INSTALL_HTTP_TIMEOUT_SECONDS=")...)
	if refused.status == 0 || !strings.Contains(refused.stderr, "MISE_INSTALL_HTTP_TIMEOUT_SECONDS") {
		t.Fatalf("an empty timeout: status %d stderr %q", refused.status, refused.stderr)
	}
}

// A Go job downloaded Go twice: setup-go fetched the release go.mod names and
// mise the one the consumer's mise config declares. One provider installs it
// now. setup-go runs only for a consumer whose mise config has no Go, and for
// mise's Go the engine of this tree writes what setup-go wrote beside its
// install.
func TestSetupToolchainInstallsGoOnce(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "setup-toolchain")

	setupGo, uses := action.step(t, "Setup Go from go.mod"), 0
	order := map[string]int{}

	for index, candidate := range action.Runs.Steps {
		order[candidate.Name] = index

		if strings.Contains(candidate.Uses, "/actions/setup-go@") {
			uses++
		}
	}

	if uses != 1 || !strings.Contains(setupGo.Uses, "/actions/setup-go@") || setupGo.With["go-version-file"] != "go.mod" ||
		setupGo.If != "hashFiles('go.mod') != '' && steps.go.outputs.provider != 'mise'" {
		t.Fatalf("%d setup-go steps, %+v; want one, from go.mod, skipped when mise provides Go", uses, setupGo)
	}

	choose := action.step(t, "go")
	if choose.If != "hashFiles('go.mod') != ''" || order[choose.Name] > order[setupGo.Name] {
		t.Fatalf("the provider step runs on %q at %d, setup-go at %d", choose.If, order[choose.Name], order[setupGo.Name])
	}

	// The consumer's installer builds with the job's Go, which mise's
	// provider puts on PATH in the exposure step.
	if order["Install consumer dev tools (cache miss)"] < order["Expose installed tools"] {
		t.Fatal("the dev-tools installer runs before the job's Go is on PATH")
	}

	for name, test := range map[string]struct{ config, output, path, env string }{
		"mise declares go": {"[tools]\ngo = \"1.26.6\"\n", "provider=mise\n", "home/go/bin\n", "GOTOOLCHAIN=local\n"},
		"mise has no go":   {"[tools]\n\"aqua:a/b\" = \"1.0.0\"\n", "provider=go.mod\n", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			box, env := engineSandbox(t, "setup-toolchain", nil)
			box.write(".mise.toml", test.config, 0o600)
			pathFile := box.write("path-file", "", 0o600)
			envFile := box.write("env-file", "", 0o600)

			got := box.run(choose.Run, append(env, "GITHUB_ACTIONS=true", "FORGEJO_PATH="+pathFile, "FORGEJO_ENV="+envFile, "GOPATH=")...)
			if got.status != 0 || box.output() != test.output {
				t.Fatalf("status %d outputs %q, want %q; stderr %s", got.status, box.output(), test.output, got.stderr)
			}

			wantPath := test.path
			if wantPath != "" {
				wantPath = filepath.Join(box.root, wantPath)
			}

			if path, env := readSandboxFile(t, box, "path-file"), readSandboxFile(t, box, "env-file"); path != wantPath || env != test.env {
				t.Fatalf("path file %q, env file %q; want %q and %q", path, env, wantPath, test.env)
			}
		})
	}
}

// The Go download ran outside the install's retry and failed a release on a
// stalled link. Through the engine of this tree a runtime install that fails
// once is tried again, and the tool install follows it.
func TestSetupToolchainRetriesTheGoInstall(t *testing.T) {
	t.Parallel()

	box, env := engineSandbox(t, "setup-toolchain", nil)
	box.write(".mise.toml", "[tools]\ngo = \"1.26.6\"\n\"aqua:a/b\" = \"1.0.0\"\n", 0o600)

	calls := filepath.Join(box.root, "mise-calls")
	box.write("home/.local/bin/mise", `#!/usr/bin/env bash
shift 2
printf '%s\n' "$*" >>"`+calls+`"
if [[ "$*" == "install go" && "$(grep -c -x 'install go' "`+calls+`")" == 1 ]]; then
  echo 'Failed to install core:go@1.26.6: HTTP timed out' >&2
  exit 1
fi
`, 0o700)

	got := box.run(loadAction(t, "setup-toolchain").step(t, "Install tools (cache miss)").Run, append(env,
		"MISE_LOCKED_INSTALL=false", "TOOLS_SUBSET=", "MISE_INSTALL_HTTP_TIMEOUT_SECONDS=120", "MISE_INSTALL_RETRY_DELAY_SECONDS=-1")...)
	if got.status != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
	}

	if recorded := readSandboxFile(t, box, "mise-calls"); recorded != "install go\ninstall go\ninstall\n" {
		t.Fatalf("mise ran %q, want the Go install twice and then the tools", recorded)
	}
}

// rustup downloads the toolchain rust-toolchain.toml pins after mise has
// installed rustup, and that download ran once. Through the engine of this
// tree a rustup call that fails once is tried again, with the install's HTTP
// timeout, and the tool install follows it.
func TestSetupToolchainRetriesTheRustupToolchainDownload(t *testing.T) {
	t.Parallel()

	box, env := engineSandbox(t, "setup-toolchain", nil)
	box.write(".mise.toml", "[tools]\n\"aqua:rust-lang/rustup\" = \"1.28.2\"\n", 0o600)
	box.write("rust-toolchain.toml", "[toolchain]\nchannel = \"1.90.0\"\n", 0o600)

	calls := filepath.Join(box.root, "mise-calls")
	box.write("home/.local/bin/mise", `#!/usr/bin/env bash
shift 2
printf '%s %s\n' "${MISE_HTTP_TIMEOUT-unset}" "$*" >>"`+calls+`"
if [[ "$*" == *" rustup "* && "$(grep -c -x -F "120s $*" "`+calls+`")" == 1 ]]; then
  echo 'error: could not download file: operation timed out' >&2
  exit 1
fi
if [[ "$*" == *" rustup which cargo" ]]; then
  echo /rustup/toolchains/1.90.0/bin/cargo
fi
`, 0o700)

	got := box.run(loadAction(t, "setup-toolchain").step(t, "Install tools (cache miss)").Run, append(env,
		"MISE_LOCKED_INSTALL=false", "TOOLS_SUBSET=", "MISE_INSTALL_HTTP_TIMEOUT_SECONDS=120", "MISE_INSTALL_RETRY_DELAY_SECONDS=-1")...)
	if got.status != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
	}

	const rustup = "120s exec --no-deps aqua:rust-lang/rustup -- rustup "

	want := "120s install aqua:rust-lang/rustup\n" +
		rustup + "show\n" + rustup + "show\n" +
		rustup + "which cargo\n" + rustup + "which cargo\n" +
		"120s install\n"
	if recorded := readSandboxFile(t, box, "mise-calls"); recorded != want {
		t.Fatalf("mise ran %q, want %q", recorded, want)
	}
}

// Exposure is per-run runner state that the cache does not restore, so the
// step must run on cache hits too.
func TestSetupToolchainExposureIsUnconditional(t *testing.T) {
	t.Parallel()

	if selected := loadAction(t, "setup-toolchain").step(t, "Expose installed tools"); selected.If != "" {
		t.Fatalf("Expose installed tools is conditional on %q", selected.If)
	}
}

// The bootstrap step requires a mise version and both musl archive digests
// from versions.env; Renovate bumps the values, not their shape.
func TestSetupToolchainBootstrapPins(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "setup-toolchain")
	versions := s.write("versions.env", string(reporoot.ReadFile(t, ".forgejo/actions/setup-toolchain/versions.env")), 0o600)
	got := s.run(`set -euo pipefail
source "${VERSIONS:?}"
printf '%s\n' "${MISE_VERSION:?}" "${MISE_LINUX_X64_MUSL_TAR_GZ_SHA256:?}" "${MISE_LINUX_ARM64_MUSL_TAR_GZ_SHA256:?}"
`, "VERSIONS="+versions)
	values := strings.Split(strings.TrimSuffix(got.stdout, "\n"), "\n")

	if got.status != 0 || len(values) != 3 || !regexp.MustCompile(`^[0-9][0-9.]*$`).MatchString(values[0]) ||
		!container.ValidSHA256Hex(values[1]) || !container.ValidSHA256Hex(values[2]) {
		t.Fatalf("versions.env pins %q (status %d, stderr %s)", values, got.status, got.stderr)
	}
}
