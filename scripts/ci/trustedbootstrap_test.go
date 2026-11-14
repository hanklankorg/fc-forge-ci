// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package ci

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These are inert local payloads, not a claimed canonical build. The real
// strict hash verifier is copied unchanged into a committed offline fixture.
func TestTrustedBootstrap(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("trusted leaf bootstrap intentionally requires native linux/amd64")
	}

	for _, scenario := range []string{"verified-new-file", "wrong-origin", "wrong-commit", "short-commit", "wrong-hash", "missing", "symlink", "symlink-parent", "nonexec", "directory", "dirty-helper", "hidden-helper-edit", "hidden-record-edit", "missing-record", "fsmonitor-verified", "fsmonitor-wrong-hash", "clean-filter-helper", "clean-filter-record", "unrelated-dirty", "dirty-tools", "hidden-tools", "missing-tools", "linked-tools", "missing-other-record"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			engine := filepath.Join(root, "engine")
			delivery := filepath.Join(root, "delivery")

			for _, dir := range []string{filepath.Join(engine, ".engine", "bin"), filepath.Join(engine, "scripts/ci"), delivery} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			write := func(path, body string, mode os.FileMode) {
				t.Helper()

				if err := os.WriteFile(path, []byte(body), mode); err != nil {
					t.Fatal(err)
				}
			}

			lib, err := os.ReadFile("../../.engine/bin/lib-forge-ci.sh")
			if err != nil {
				t.Fatal(err)
			}

			library := filepath.Join(engine, ".engine", "bin", "lib-forge-ci.sh")
			write(library, string(lib), 0o600)

			tools, err := os.ReadFile("trusted-runtime-tools.sh")
			if err != nil {
				t.Fatal(err)
			}

			toolHelper := filepath.Join(engine, "scripts/ci/trusted-runtime-tools.sh")
			write(toolHelper, string(tools), 0o600)

			body := "#!/bin/bash\nset -eu\nprintf 'verified:%s:%s:%s:%s:%s\\n' \"$1\" \"${FORGE_CI_BIN-unset}\" \"${FORGE_CI_PATH-unset}\" \"${FORGE_CI_BINARY_REF-unset}\" \"${FORGE_CI_PLAN-unset}\" >>\"$PROCESS_MARKER\"\nprintf '%s\\n' \"$ADMIN_INPUT\"\n"
			if scenario == "verified-new-file" {
				// Map the actual helper's fixed PATH into a disposable image-layout
				// fixture. No host /usr writes, containers or production lookup seam.
				body += runtimeToolLookupFixture

				for name, directory := range map[string]string{"go": "/usr/local/go/bin", "cargo": "/usr/local/cargo/bin", "rustc": "/usr/local/cargo/bin", "gh": "/usr/local/bin", "cosign": "/usr/local/bin", "git-cliff": "/usr/local/bin", "forge-ci": "/usr/local/bin"} {
					path := filepath.Join(root, "image", directory, name)
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}

					write(path, "#!/bin/bash\nprintf '%s\\n' '"+name+"' >>\"$TOOL_MARKER\"\n", 0o700)
				}
			}

			binary := filepath.Join(delivery, "forge-ci-linux-amd64")
			write(binary, body, 0o700)

			record := filepath.Join(engine, ".engine", "bin", "forge-ci-linux-amd64.sha256")
			write(record, fmt.Sprintf("%x  bin/forge-ci\n", sha256.Sum256([]byte(body))), 0o600)
			// The other architecture's record is consumed by the loop too: a
			// checkout missing it is not the approved engine.
			otherRecord := filepath.Join(engine, ".engine", "bin", "forge-ci-linux-arm64.sha256")
			write(otherRecord, fmt.Sprintf("%x  bin/forge-ci\n", sha256.Sum256([]byte(body))), 0o600)

			git := func(args ...string) string {
				t.Helper()

				cmd := exec.CommandContext(t.Context(), "/usr/bin/git", append([]string{"-C", engine}, args...)...)
				cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}

				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}

				return strings.TrimSpace(string(out))
			}
			git("init", "-q")

			origin := "https://approved.invalid/admin/engine"
			git("remote", "add", "origin", origin+".git")
			git("add", ".")
			git("-c", "user.name=Offline", "-c", "user.email=offline@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
			commit := git("rev-parse", "HEAD")
			marker := filepath.Join(root, "processes")
			gitMarker := filepath.Join(root, "git-executed")
			// Absolute marker paths: engine_git intentionally drops ambient env.
			monitor := filepath.Join(root, "fsmonitor")
			write(monitor, "#!/bin/sh\nprintf fsmonitor >>'"+gitMarker+"'\n", 0o700)

			filter := filepath.Join(root, "clean-filter")
			write(filter, "#!/bin/sh\nprintf filter >>'"+gitMarker+"'\ncat\n", 0o700)

			hostile := filepath.Join(root, "hostile")
			if err := os.Mkdir(hostile, 0o700); err != nil {
				t.Fatal(err)
			}
			// Older SDK/PATH CLI must never be selected, nor any hostile tools.
			for _, name := range []string{"forge-ci", "git", "sha256sum", "uname", "realpath", "dirname", "env"} {
				write(filepath.Join(hostile, name), "#!/bin/bash\nprintf hostile >>\"$PROCESS_MARKER\"\nexit 99\n", 0o700)
			}

			switch scenario {
			case "dirty-tools", "hidden-tools":
				if scenario == "hidden-tools" {
					git("update-index", "--assume-unchanged", "scripts/ci/trusted-runtime-tools.sh")
				}

				write(toolHelper, "printf sourced >>\"$PROCESS_MARKER\"\n"+string(tools), 0o600)
			case "missing-tools":
				if err := os.Remove(toolHelper); err != nil {
					t.Fatal(err)
				}
			case "linked-tools":
				if err := os.Rename(toolHelper, toolHelper+".real"); err != nil {
					t.Fatal(err)
				}

				if err := os.Symlink(toolHelper+".real", toolHelper); err != nil {
					t.Fatal(err)
				}
			case "wrong-origin":
				origin = "https://unapproved.invalid/admin/engine"
			case "wrong-commit":
				commit = strings.Repeat("1", 40)
			case "short-commit":
				commit = commit[:8]
			case "wrong-hash", "fsmonitor-wrong-hash":
				write(binary, "#!/bin/bash\nprintf wrong >>\"$PROCESS_MARKER\"\n", 0o700)
			case "missing":
				if err := os.Remove(binary); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				binary = filepath.Join(delivery, "link")
				if err := os.Symlink("forge-ci-linux-amd64", binary); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				link := filepath.Join(root, "delivery-link")
				if err := os.Symlink(delivery, link); err != nil {
					t.Fatal(err)
				}

				binary = filepath.Join(link, "forge-ci-linux-amd64")
			case "nonexec":
				if err := os.Chmod(binary, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				binary = delivery
			case "dirty-helper", "hidden-helper-edit":
				if scenario == "hidden-helper-edit" {
					git("update-index", "--assume-unchanged", ".engine/bin/lib-forge-ci.sh")
				}

				write(library, "printf sourced >>\"$PROCESS_MARKER\"\n"+string(lib), 0o600)
			case "hidden-record-edit":
				git("update-index", "--skip-worktree", ".engine/bin/forge-ci-linux-amd64.sha256")
				write(record, strings.Repeat("0", 64)+"  bin/forge-ci\n", 0o600)
			case "missing-record":
				if err := os.Remove(record); err != nil {
					t.Fatal(err)
				}
			case "missing-other-record":
				if err := os.Remove(otherRecord); err != nil {
					t.Fatal(err)
				}
			case "clean-filter-helper", "clean-filter-record":
				git("config", "filter.marker.clean", filter)
				git("config", "filter.marker.required", "true")
				write(filepath.Join(engine, ".gitattributes"), ".engine/bin/* filter=marker\n", 0o600)

				if scenario == "clean-filter-helper" {
					write(library, "printf sourced >>\"$PROCESS_MARKER\"\n"+string(lib), 0o600)
				} else {
					write(record, strings.Repeat("0", 64)+"  bin/forge-ci\n", 0o600)
				}
			case "unrelated-dirty":
				write(filepath.Join(engine, "unrelated"), "not consumed\n", 0o600)
			}

			if strings.HasPrefix(scenario, "fsmonitor-") {
				git("config", "core.fsmonitor", monitor)
			}

			helper, err := filepath.Abs("trusted-bootstrap.sh")
			if err != nil {
				t.Fatal(err)
			}

			cmd := exec.CommandContext(t.Context(), "/bin/bash", "--noprofile", "--norc", helper, engine, origin, commit, binary, "runtime", "--", "--version")
			cmd.Env = []string{"PATH=" + hostile, "HOME=" + root, "PROCESS_MARKER=" + marker, "ADMIN_INPUT=retained", "FORGE_CI_BIN=" + filepath.Join(hostile, "forge-ci"), "FORGE_CI_PATH=" + hostile, "FORGE_CI_BINARY_REF=old-sdk", "FORGE_CI_PLAN=/consumer/plan.json", "HELPERS_ENGINE_VERIFIED=" + binary, "SIGNER_STRICT=0", "GIT_DIR=/consumer/git", "GIT_WORK_TREE=/consumer", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=remote.origin.url", "GIT_CONFIG_VALUE_0=https://hostile.invalid/engine", "TEST_IMAGE_ROOT=" + filepath.Join(root, "image"), "TOOL_MARKER=" + filepath.Join(root, "tools"), "CARGO_HOME=/consumer/cache", "RUSTUP_HOME=/consumer/toolchain", "TRUSTED_TOOL_PATH=" + hostile}

			out, runErr := cmd.CombinedOutput()
			if invoked, err := os.ReadFile(gitMarker); !os.IsNotExist(err) {
				t.Fatalf("Git executed repository-local machinery: %q (%v), output %s", invoked, err, out)
			}

			processes, readErr := os.ReadFile(marker)

			if scenario == "verified-new-file" || scenario == "fsmonitor-verified" || scenario == "unrelated-dirty" {
				if runErr != nil {
					t.Fatalf("bootstrap: %v\n%s", runErr, out)
				}

				if string(processes) != "verified:--version:unset:unset:unset:unset\n" || !strings.Contains(string(out), "retained") {
					t.Fatalf("wrong executable/environment: %q output %s", processes, out)
				}

				if scenario == "verified-new-file" {
					tools, err := os.ReadFile(filepath.Join(root, "tools"))
					if err != nil || string(tools) != "gh\ngo\ncargo\nrustc\ncosign\ngit-cliff\n" {
						t.Fatalf("approved image tools not reached (or SDK forge-ci executed): %q, %v", tools, err)
					}
				}
			} else {
				if runErr == nil {
					t.Fatalf("accepted %s: %s", scenario, out)
				}

				if !os.IsNotExist(readErr) {
					t.Fatalf("process/source ran before refusal: %q (%v), output %s", processes, readErr, out)
				}
			}
		})
	}
}

// The orchestrator is deliberately outside this unit. Its consumer checkout
// and private-environment ordering must be guarded when that leaf is wired.
// Here guard the actual helper's identity -> source -> verify -> exec order.
func TestTrustedBootstrapOrdering(t *testing.T) {
	body, err := os.ReadFile("trusted-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}

	previous := -1

	for _, step := range []string{
		`engine_git rev-parse --verify HEAD`,
		`engine_git config --local --no-includes --get remote.origin.url`,
		`engine_git hash-object --no-filters`,
		`source "$checkout/.engine/bin/lib-forge-ci.sh"`,
		`SIGNER_STRICT=1`,
		`verify_forge_ci || exit $?`,
		`exec /bin/bash -p "$checkout/scripts/ci/trusted-runtime-tools.sh" "$profile" "$HELPERS_ENGINE_VERIFIED" "$@"`,
	} {
		position := strings.Index(string(body), step)
		if position <= previous {
			t.Fatalf("missing/out-of-order bootstrap boundary: %s", step)
		}

		previous = position
	}
	// Keep executable worktree/index inspection out of this narrow boundary.
	for _, forbidden := range []string{"engine_git status", "engine_git diff", "engine_git submodule", "engine_git checkout"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("executable Git machinery in bootstrap: %s", forbidden)
		}
	}
}
