// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejobehavior

import (
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSnapshotBuilderPrivateAuthCleanupOnFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"login", "admission", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			program := `set -euo pipefail
source "$1"
snapshot_engine() {
  case "$1 $2" in
  'container login')
    test "$4" = registry.example
    test "$(cat)" = job-token-canary
    printf '%s' "${10}" >auth-path
    : >"${10}"
    if [ "$MODE" = login ]; then return 69; fi
    ;;
  'container setup-image-builder')
    if [ "$MODE" = cancel ]; then kill -TERM "$BASHPID"; fi
    return 78
    ;;
  *) touch unexpected-build; return 99 ;;
  esac
}
snapshot_setup_builder kaniko amd64
`
			cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
			cmd.Dir = root
			cmd.Env = snapshotTestEnvironment(t, "FORGE_CI_PROVIDER=gitlab", "RC_REGISTRY=registry.example", "CI_REGISTRY=attacker.invalid", "CI_JOB_TOKEN=job-token-canary", "MODE="+mode)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", out)
			path, err := os.ReadFile(filepath.Join(root, "auth-path"))
			require.NoError(t, err)
			require.NoFileExists(t, string(path))
			require.NoDirExists(t, filepath.Dir(string(path)))
			require.NoFileExists(t, filepath.Join(root, "unexpected-build"))
		})
	}
}
