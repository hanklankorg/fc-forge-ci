// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package ci

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCanonicalConsumerRealHelpers(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("canonical bootstrap currently supports native amd64")
	}

	deliveryBody, err := os.ReadFile("canonical-cli-delivery.sh")
	if err != nil {
		t.Fatal(err)
	}

	for _, scenario := range []string{"valid-hostile-sdk", "failed-producer", "stale", "missing-id", "missing-dependency", "foreign-metadata", "bad-hash", "missing-binary", "unknown-profile"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			workspace, temp := filepath.Join(root, "workspace"), filepath.Join(root, "temp")
			engine := filepath.Join(workspace, "_forge_ci_bootstrap")
			write := func(path string, body []byte, mode os.FileMode) {
				t.Helper()

				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(path, body, mode); err != nil {
					t.Fatal(err)
				}
			}

			if err := os.MkdirAll(temp, 0o700); err != nil {
				t.Fatal(err)
			}

			for target, source := range map[string]string{"scripts/ci/engine-binary.sh": "engine-binary.sh", "scripts/ci/trusted-bootstrap.sh": "trusted-bootstrap.sh", "scripts/ci/trusted-runtime-tools.sh": "trusted-runtime-tools.sh", "scripts/ci/canonical-consumer.sh": "canonical-consumer.sh", ".engine/bin/lib-forge-ci.sh": "../../.engine/bin/lib-forge-ci.sh"} {
				body, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}

				write(filepath.Join(engine, target), body, 0o600)
			}

			if scenario != "missing-dependency" {
				write(filepath.Join(engine, "scripts/ci/canonical-cli-delivery.sh"), deliveryBody, 0o600)
			}

			body := []byte("#!/bin/bash\nprintf 'verified:%s\\n' \"$1\" >>\"$PROCESS_MARKER\"\n")
			for _, arch := range []string{"amd64", "arm64"} {
				write(filepath.Join(engine, ".engine/bin/forge-ci-linux-"+arch+".sha256"), []byte(fmt.Sprintf("%x  bin/forge-ci\n", sha256.Sum256(body))), 0o600)
			}

			git := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "/usr/bin/git", append([]string{"-C", engine}, args...)...)
				cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}

				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git: %v: %s", err, out)
				}

				return strings.TrimSpace(string(out))
			}
			git("init", "-q")
			git("remote", "add", "origin", "https://approved.invalid/approved/engine.git")
			git("add", ".")
			git("-c", "user.name=Offline", "-c", "user.email=offline@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "inert fixture")
			commit := git("rev-parse", "HEAD")

			hostile := filepath.Join(root, "sdk")
			for _, name := range []string{"forge-ci", "git", "sha256sum", "gh", "realpath"} {
				write(filepath.Join(hostile, name), []byte("#!/bin/bash\nprintf hostile >>\"$PROCESS_MARKER\"\nexit 97\n"), 0o700)
			}

			env := map[string]string{"PATH": hostile, "HOME": root, "RUNNER_TEMP": temp, "GITHUB_WORKSPACE": workspace, "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_REPOSITORY_ID": "100", "GITHUB_SHA": strings.Repeat("a", 40), "GITHUB_SERVER_URL": "https://approved.invalid", "ENGINE_REPOSITORY": "approved/engine", "ENGINE_COMMIT": commit, "ENGINE_ORIGIN": "https://approved.invalid/approved/engine", "RUNTIME_IMAGE": "registry.invalid/sdk@sha256:" + strings.Repeat("a", 64), "PRODUCER_ARTIFACT_ID": "456", "PRODUCER_RUN_ID": "123", "PRODUCER_RUN_ATTEMPT": "2", "PRODUCER_ENGINE_REPOSITORY": "approved/engine", "PRODUCER_ENGINE_COMMIT": commit, "PRODUCER_ARTIFACT_NAME": "forge-ci-canonical-linux-123-2-" + commit, "TOOL_PROFILE": "host", "GITHUB_PATH": filepath.Join(root, "path"), "GITHUB_OUTPUT": filepath.Join(root, "output"), "PROCESS_MARKER": filepath.Join(root, "processes"), "FORGE_CI_PLAN": "/consumer/plan", "FORGE_CI_BIN": filepath.Join(hostile, "forge-ci")}

			env["CANONICAL_PRODUCER_RESULT"] = "success"
			if scenario == "failed-producer" {
				env["CANONICAL_PRODUCER_RESULT"] = "failure"
			}

			if scenario == "stale" {
				env["PRODUCER_RUN_ATTEMPT"] = "1"
			}

			if scenario == "missing-id" {
				env["PRODUCER_ARTIFACT_ID"] = ""
			}

			if scenario == "unknown-profile" {
				// Regex-valid, so the glue passes it on; only the tool-profile owner
				// knows the finite set, and it refuses at the first verified probe.
				env["TOOL_PROFILE"] = "hosted"
			}

			run := func(script string, args ...string) error {
				t.Helper()

				cmd := exec.CommandContext(t.Context(), "/bin/bash", append([]string{"-p", script}, args...)...)
				for key, value := range env {
					cmd.Env = append(cmd.Env, key+"="+value)
				}

				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Logf("expected-or-diagnostic refusal: %s", out)
				}

				return err
			}
			initial := filepath.Join(engine, "scripts/ci/canonical-consumer.sh")

			prepareErr := run(initial, "prepare")
			if scenario == "failed-producer" || scenario == "stale" || scenario == "missing-id" || scenario == "missing-dependency" {
				if prepareErr == nil {
					t.Fatal("invalid original context passed prepare")
				}
			} else {
				if prepareErr != nil {
					t.Fatal(prepareErr)
				}

				metadata := map[string]any{"id": 456, "name": env["PRODUCER_ARTIFACT_NAME"], "expired": false, "size_in_bytes": 100, "workflow_run": map[string]any{"id": 123, "repository_id": 100, "head_sha": env["GITHUB_SHA"]}}
				if scenario == "foreign-metadata" {
					metadata["id"] = 457
				}

				encoded, err := json.Marshal(metadata)
				if err != nil {
					t.Fatal(err)
				}

				write(filepath.Join(temp, "forge-ci-canonical-metadata.json"), encoded, 0o600)
				helper := filepath.Join(temp, "forge-ci-approved-engine/scripts/ci/canonical-consumer.sh")

				admitErr := run(helper, "admit")
				if scenario == "foreign-metadata" {
					if admitErr == nil {
						t.Fatal("foreign metadata passed")
					}
				} else {
					if admitErr != nil {
						t.Fatal(admitErr)
					}

					binary := filepath.Join(temp, "forge-ci-canonical-123-2/forge-ci-linux-amd64")
					write(filepath.Join(temp, "forge-ci-canonical-123-2/forge-ci-linux-arm64"), body, 0o600)

					if scenario != "missing-binary" {
						if scenario == "bad-hash" {
							body = []byte("#!/bin/bash\nprintf wrong >>\"$PROCESS_MARKER\"\n")
						}

						write(binary, body, 0o600)
					}

					receiveErr := run(helper, "receive")

					if scenario == "unknown-profile" {
						var exit *exec.ExitError
						if !errors.As(receiveErr, &exit) || exit.ExitCode() != 78 {
							t.Fatalf("an unknown tool profile must refuse with exit 78, got %v", receiveErr)
						}

						if _, err := os.Lstat(filepath.Join(temp, "forge-ci-canonical-bin")); !os.IsNotExist(err) {
							t.Fatal("a launcher was written for an unknown tool profile")
						}
					}

					if scenario == "valid-hostile-sdk" {
						if receiveErr != nil {
							t.Fatal(receiveErr)
						}
						// Consumer checkout may replace the entire workspace, not the
						// independently retained engine/delivery/strict launcher.
						write(filepath.Join(workspace, "forge-ci"), []byte("consumer garbage"), 0o600)

						if err := run(filepath.Join(temp, "forge-ci-canonical-bin/forge-ci"), "plan"); err != nil {
							t.Fatal(err)
						}

						processes, err := os.ReadFile(env["PROCESS_MARKER"])
						if err != nil || string(processes) != "verified:--version\nverified:plan\n" {
							t.Fatalf("processes=%q err=%v", processes, err)
						}

						write(binary, []byte("#!/bin/bash\nprintf tampered >>\"$PROCESS_MARKER\"\n"), 0o700)

						if run(filepath.Join(temp, "forge-ci-canonical-bin/forge-ci"), "plan") == nil {
							t.Fatal("launcher failed to repeat strict verification")
						}

						after, err := os.ReadFile(env["PROCESS_MARKER"])
						if err != nil || string(after) != string(processes) {
							t.Fatal("tampered launcher input executed")
						}

						return
					}

					if receiveErr == nil {
						t.Fatal("invalid bytes passed receive")
					}
				}
			}

			for _, path := range []string{env["PROCESS_MARKER"], env["GITHUB_PATH"], env["GITHUB_OUTPUT"]} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("preflight failure exposed invocation/launcher/completion: %s", path)
				}
			}
		})
	}
}
