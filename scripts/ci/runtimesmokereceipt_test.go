// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestRuntimeSmokeReceiptDelegatesAndCleansUp(t *testing.T) {
	t.Parallel()

	for _, status := range []int{0, 37} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			ref := "registry.example/acme/runtime@sha256:" + strings.Repeat("b", 64)

			tool := filepath.Join(root, "forge-ci")
			require.NoError(t, os.WriteFile(tool, []byte(`#!/bin/sh
set -eu
if [ "$1" = artifact ]; then
  test "$1 $2 $3 $4 $5" = 'artifact download --name runtime-smoke-full-amd64 --dir'
  mkdir -p "$6"
  printf '{}\n' >"$6/receipt.json"
else
  test -f "$5"
  printf '%s\n' "$@" >"$FORWARD_RECORD"
  exit "$VERIFY_STATUS"
fi
`), 0o600))
			require.NoError(t, os.Chmod(tool, 0o700))
			cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), "scripts/ci/runtime-images.sh"), "verify-smoke", "full", "amd64", ref)
			record := filepath.Join(root, "forwarded")
			cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "FORWARD_RECORD="+record, "VERIFY_STATUS="+strconv.Itoa(status), "SOURCE_SHA="+strings.Repeat("a", 40), "RUNNER_TEMP="+root)

			out, err := cmd.CombinedOutput()
			if status == 0 {
				require.NoError(t, err, "%s", out)
			} else {
				require.Error(t, err, "%s", out)
				require.Equal(t, status, cmd.ProcessState.ExitCode())
			}

			forwarded, err := os.ReadFile(record)
			require.NoError(t, err)

			args := strings.Split(strings.TrimSpace(string(forwarded)), "\n")
			require.Len(t, args, 13)
			require.True(t, strings.HasPrefix(args[4], filepath.Join(root, "runtime-smoke-check.")))
			require.Equal(t, []string{"container", "runtime-publication", "verify-smoke", "--receipt", args[4], "--source-sha", strings.Repeat("a", 40), "--target", "full", "--arch", "amd64", "--image-ref", ref}, args)

			leftovers, err := filepath.Glob(filepath.Join(root, "runtime-smoke-check.*"))
			require.NoError(t, err)
			require.Empty(t, leftovers)
		})
	}
}
