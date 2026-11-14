// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestBootstrapDownloadPreservesVerificationAndCleanup(t *testing.T) {
	t.Parallel()

	// Simulate transport outcomes without contacting an upstream or sleeping.
	const script = `set -euo pipefail
source "$1"
destination="$2/download"
calls=0
curl() {
    calls=$((calls + 1))
    printf abc > "$destination"
    return "$status"
}
digest=ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
status=22
if ci_download_verified https://example.invalid/tool "$destination" "$digest"; then exit 1; fi
test ! -e "$destination"
status=0
if ci_download_verified https://example.invalid/tool "$destination" "${digest/a/b}"; then exit 1; fi
test ! -e "$destination"
ci_download_verified https://example.invalid/tool "$destination" "$digest"
test "$(cat "$destination")" = abc
test "$calls" = 3
if ci_download_verified https://example.invalid/tool "$destination" invalid; then exit 1; fi
test "$calls" = 3
`

	command := exec.CommandContext(t.Context(), "bash", "-c", script, "--",
		filepath.Join(reporoot.Path(t), "scripts/bootstrap/install-common.sh"), t.TempDir())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("download contract: %v\n%s", err, output)
	}
}
