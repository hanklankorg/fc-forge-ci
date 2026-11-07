// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestCanonicalRuntimeArtifactVerification(t *testing.T) {
	t.Parallel()

	for _, arch := range engineArchitectures() {
		for _, mode := range []string{"canonical", "stamped release", "architecture substitution", "malformed record", "missing record", "absent"} {
			t.Run(arch+"/"+mode, func(t *testing.T) {
				t.Parallel()

				box := newSandbox(t, "runtime-cli-identity")
				box.write(binaryScript, string(reporoot.ReadFile(t, binaryScript)), 0o700)

				body := "owned canonical delivery fixture for " + arch
				digest := sha256.Sum256([]byte(body))

				record := hex.EncodeToString(digest[:]) + "  bin/forge-ci\n"
				if mode == "malformed record" {
					record = "invalid  bin/forge-ci\n"
				}

				if mode != "missing record" {
					box.write(trackedChecksum(arch), record, 0o600)
				}

				if mode == "stamped release" {
					body += " with version/commit/date stamps"
				}

				if mode == "architecture substitution" {
					other := "amd64"
					if arch == other {
						other = "arm64"
					}

					body = "owned canonical delivery fixture for " + other
				}

				binary := box.write("artifact", body, 0o600)
				if mode == "absent" {
					binary = filepath.Join(box.root, "absent")
				}

				got := box.run("bash scripts/ci/engine-binary.sh verify '"+arch+"' '"+binary+"'", "GO=/nonexistent/no-compiler-is-needed")
				if (got.status == 0) != (mode == "canonical") {
					t.Fatalf("status=%d stdout=%s stderr=%s", got.status, got.stdout, got.stderr)
				}

				if mode == "stamped release" && !strings.Contains(got.stderr, "does not match the recorded delivery checksum") {
					t.Fatalf("wrong binary did not fail at identity gate: %s", got.stderr)
				}
			})
		}
	}
}

func TestRuntimeArtifactStageRequiresCompleteUniqueCanonicalInventory(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"canonical", "missing artifact", "missing architecture", "duplicate artifact", "nested duplicate", "missing record", "architecture substitution"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "runtime-cli-stage")
			box.write(binaryScript, string(reporoot.ReadFile(t, binaryScript)), 0o700)

			for _, arch := range engineArchitectures() {
				body := "owned canonical delivery fixture for " + arch
				digest := sha256.Sum256([]byte(body))

				if mode != "missing record" || arch != "arm64" {
					box.write(trackedChecksum(arch), hex.EncodeToString(digest[:])+"  bin/forge-ci\n", 0o600)
				}

				if mode == "missing artifact" || mode == "missing architecture" && arch == "arm64" {
					continue
				}

				if mode == "architecture substitution" && arch == "arm64" {
					body = "owned canonical delivery fixture for amd64"
				}

				box.write("engine-cli/forge-ci-linux-"+arch, body, 0o600)
			}

			if mode == "duplicate artifact" {
				box.write("engine-cli/forge-ci-linux-arm64.1", "duplicate", 0o600)
			}

			if mode == "nested duplicate" {
				box.write("engine-cli/duplicate/forge-ci-linux-arm64", "duplicate", 0o600)
			}

			got := box.run("bash scripts/ci/engine-binary.sh stage arm64 engine-cli staged-engine", "GO=/nonexistent/no-compiler-is-needed")
			staged, err := os.ReadFile(filepath.Join(box.root, "staged-engine"))

			if mode == "canonical" {
				if got.status != 0 || err != nil || string(staged) != "owned canonical delivery fixture for arm64" {
					t.Fatalf("canonical stage failed: status=%d data=%q err=%v stderr=%s", got.status, staged, err, got.stderr)
				}
			} else if got.status == 0 || !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid artifact was staged: status=%d err=%v stderr=%s", got.status, err, got.stderr)
			}
		})
	}
}
