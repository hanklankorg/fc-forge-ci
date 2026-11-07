// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestCargoContractCollaboratorIsExposedOnlyAfterChecksumVerification(t *testing.T) {
	t.Parallel()
	step := jobStep(t, ".github/workflows/build-cargo.yml", "build-cargo", "Verify the native test engine collaborator")

	for _, mode := range []string{"matching", "mismatch", "malformed checksum", "missing binary", "symlink binary"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			box, digest, starts := setupForgeCISandbox(t, "exit 0")
			box.write(".forge-ci-contract-engine/.engine/bin/lib-forge-ci.sh", string(reporoot.ReadFile(t, ".engine/bin/lib-forge-ci.sh")), 0o600)
			binary := filepath.Join(box.root, ".engine/bin/forge-ci")
			checksum := digest + "  bin/forge-ci\n"

			switch mode {
			case "mismatch":
				checksum = strings.Repeat("0", 64) + "  bin/forge-ci\n"
			case "malformed checksum":
				checksum = "not a checksum\n"
			case "missing binary":
				binary = filepath.Join(box.root, "absent")
			case "symlink binary":
				link := filepath.Join(box.root, "linked-engine")
				if err := os.Symlink(binary, link); err != nil {
					t.Fatal(err)
				}

				binary = link
			}

			box.write(".forge-ci-contract-engine/.engine/bin/forge-ci-linux-"+runtime.GOARCH+".sha256", checksum, 0o600)

			got := box.run(step.Run, "FORGE_CI_BIN="+binary, "SIGNER_STRICT="+step.Env["SIGNER_STRICT"], "GITHUB_OUTPUT="+box.outputs)
			if mode == "matching" {
				if got.status != 0 || box.output() != "binary-path="+binary+"\n" {
					t.Fatalf("verified engine not exposed: status=%d outputs=%s stderr=%s", got.status, box.output(), got.stderr)
				}
			} else if got.status == 0 || box.output() != "" {
				t.Fatalf("unverified engine exposed: status=%d outputs=%s stderr=%s", got.status, box.output(), got.stderr)
			}

			if _, err := os.Stat(starts); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("verification executed the collaborator: %v", err)
			}
		})
	}
}
