// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// fakeTrivy fails its first $FAKE_TRIVY_FAILURES calls with status 1, as
// Trivy does when its database download fails, and then writes an empty
// report. It counts its calls in $FAKE_TRIVY_CALLS, using shell builtins
// only: the test's PATH holds nothing else. It refuses to scan when the
// signing key is in its environment, which the command must have removed.
const fakeTrivy = `#!/bin/sh
echo call >>"$FAKE_TRIVY_CALLS"
if [ -n "${COSIGN_KEY:-}" ]; then
	echo 'a signer secret reached the scanner' >&2
	exit 3
fi
calls=0
while read -r _; do calls=$((calls + 1)); done <"$FAKE_TRIVY_CALLS"
output=
while [ $# -gt 0 ]; do
	if [ "$1" = --output ]; then output=$2; fi
	shift
done
if [ "$calls" -le "$FAKE_TRIVY_FAILURES" ]; then
	echo 'FATAL: failed to download vulnerability DB' >&2
	exit 1
fi
printf '{"Results":[]}' >"$output"
`

// `container image-evidence` through the root command with a trivy on PATH:
// a scan that fails transiently passes within the default bound of three
// attempts, --trivy-attempts shortens the bound, and a scan that stays failed
// publishes no report.
func TestImageEvidenceRoot_RetriesTheScan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failures  int
		flags     []string
		wantCalls int
		wantErr   bool
	}{
		{name: "default bound covers one transient failure", failures: 1, wantCalls: 2},
		{name: "bound exhausted", failures: 2, flags: []string{"--trivy-attempts", "2"}, wantCalls: 2, wantErr: true},
		{name: "one attempt", failures: 1, flags: []string{"--trivy-attempts", "1"}, wantCalls: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			bin := filepath.Join(work, "bin")
			layout := filepath.Join(work, "layout")

			require.NoError(t, os.Mkdir(bin, 0o750))
			require.NoError(t, os.Mkdir(layout, 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "trivy"), []byte(fakeTrivy), 0o700)) //nolint:gosec // G306: the fake scanner must be executable.

			calls := filepath.Join(work, "calls")
			report := filepath.Join(work, "trivy.json")

			t.Setenv("PATH", bin)
			t.Setenv("FAKE_TRIVY_CALLS", calls)
			t.Setenv("FAKE_TRIVY_FAILURES", strconv.Itoa(tc.failures))
			t.Setenv("COSIGN_KEY", "signer-secret")

			command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
			command.Writer, command.ErrWriter = os.Stdout, os.Stderr

			err := command.Run(t.Context(), append([]string{"forge-ci", "container", "image-evidence",
				"--oci-layout", layout, "--trivy-output", report, "--trivy-retry-delay-seconds", "1"}, tc.flags...))

			recorded, readErr := os.ReadFile(calls)
			require.NoError(t, readErr)
			require.Equal(t, tc.wantCalls, strings.Count(string(recorded), "call\n"))

			if tc.wantErr {
				require.ErrorIs(t, err, errs.ErrDependencyUnavailable)
				require.NoFileExists(t, report)

				return
			}

			require.NoError(t, err)

			body, readErr := os.ReadFile(report)
			require.NoError(t, readErr)
			require.JSONEq(t, `{"Results":[]}`, string(body))
		})
	}
}

func TestImageEvidenceRoot_RefusesANegativeAttemptBound(t *testing.T) {
	layout := t.TempDir()
	t.Setenv("PATH", t.TempDir())

	command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
	command.Writer, command.ErrWriter = os.Stdout, os.Stderr

	err := command.Run(t.Context(), []string{"forge-ci", "container", "image-evidence",
		"--oci-layout", layout, "--trivy-output", filepath.Join(t.TempDir(), "trivy.json"), "--trivy-attempts", "-1"})
	require.ErrorIs(t, err, errs.ErrUsage)
}
