// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

func TestRootBaseCanonicalHandoffUsesVerifiedEvidenceRoot(t *testing.T) {
	t.Parallel()

	for _, defect := range []string{"none", "changed evidence", "reserved ledger", "preseeded destination", "unknown mode"} {
		t.Run(defect, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			payload := filepath.Join(dir, "handoff", "payload")
			require.NoError(t, os.MkdirAll(filepath.Join(payload, "dist"), 0o700))
			require.NoError(t, os.Mkdir(filepath.Join(payload, "premade-sboms"), 0o700))
			evidence := filepath.Join(payload, "premade-sboms", "base-sbom-opaque.cyclonedx.json")
			require.NoError(t, os.WriteFile(evidence, []byte("producer bytes\n"), 0o600))

			digest, err := apprelease.CanonicalPayloadDigest(payload)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "handoff", "digest"), []byte(digest), 0o600))

			mode := "canonical-payload"

			switch defect {
			case "changed evidence":
				require.NoError(t, os.WriteFile(evidence, []byte("replacement"), 0o600))
			case "reserved ledger":
				require.NoError(t, os.WriteFile(filepath.Join(payload, "dist", "base-images-publication.json"), []byte("[]"), 0o600))
			case "preseeded destination":
				require.NoError(t, os.Mkdir(filepath.Join(dir, "verified-payload"), 0o700))
			case "unknown mode":
				mode = "unknown"
			}

			const invocation = `set -eu; source "$1"
rc_engine() { OWNER_CLI_HELPER=1 "$OWNER_CLI_BINARY" -test.run='^TestOwnerCLI$' -- "$@"; }
if rc_read_handoff handoff "$2"; then
  test "$DIST_DIR" = verified-payload/dist
  test "$RC_EVIDENCE_ROOT" = verified-payload
  test "$(cat "$RC_EVIDENCE_ROOT/premade-sboms/base-sbom-opaque.cyclonedx.json")" = 'producer bytes'
else
  status=$?
  test -z "${DIST_DIR:-}${RC_EVIDENCE_ROOT:-}"
  exit "$status"
fi`

			command := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-c", invocation,
				"root-base-handoff", filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh"), mode)
			command.Dir = dir
			command.Env = []string{"PATH=" + componentTools(t), "HOME=" + dir, "OWNER_CLI_BINARY=" + os.Args[0]}

			output, err := command.CombinedOutput()
			if defect == "none" {
				require.NoError(t, err, string(output))
			} else {
				require.Error(t, err, string(output))
			}
		})
	}
}
