// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/stretchr/testify/require"
)

func TestWorkflowWaitStopsOnCancellationBeforeCleanupGraceExpires(t *testing.T) {
	for _, delay := range []time.Duration{0, 20 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				if delay == 0 {
					cancel()
				} else {
					go func() {
						time.Sleep(delay)
						cancel()
					}()
				}

				started := time.Now()

				defer func() {
					failure, ok := recover().(acceptanceFailure)
					if !ok || !strings.Contains(string(failure), "context canceled") {
						t.Errorf("workflow wait did not report cancellation: %v", failure)
					}

					if elapsed := time.Since(started); elapsed > delay {
						t.Errorf("canceled workflow wait consumed cleanup time: %s", elapsed)
					}
				}()

				// The empty forge selects no HTTP route: only polling and its
				// cancellation lifetime are exercised, never a real provider.
				waitForRun(ctx, stopOnFatalTB{TB: t}, Target{}, "fci-inert", "cancellation fixture")
				t.Error("canceled workflow wait returned a normal conclusion")
			})
		})
	}
}

func TestProbePrelude_VerifiesRunnerDigestAndVersionBeforeInvocation(t *testing.T) {
	digest := strings.Repeat("a", 64)
	t.Setenv(runnerBinarySHA256Env, digest)

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caFile, independentCAPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}

	target := Target{CAFile: caFile, accepted: true}

	script := ProbePrelude(target, "https://fixture.invalid/forge-ci")
	digestCheck := "printf '%s  forge-ci\\n' '" + digest + "' | sha256sum --check --strict"
	digestAt := strings.Index(script, digestCheck)
	versionAt := strings.Index(script, "./forge-ci --version")

	runAt := strings.Index(script, "run_product()")
	if digestAt < 0 || versionAt < digestAt || runAt < versionAt {
		t.Fatalf("runner verification order is digest=%d version=%d invocation=%d", digestAt, versionAt, runAt)
	}
}

func TestProbePrelude_RejectsMalformedRunnerDigest(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caFile, independentCAPEM(t), 0o600))
	target := Target{CAFile: caFile, accepted: true}

	t.Setenv(runnerBinarySHA256Env, strings.Repeat("a", 64))
	require.NotEmpty(t, ProbePrelude(target, "https://fixture.invalid/forge-ci"))

	for _, digest := range []string{"", "ABC", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 63), strings.Repeat("a", 65)} {
		t.Run("digest="+digest, func(t *testing.T) {
			t.Setenv(runnerBinarySHA256Env, digest)
			defer func() {
				if recovered := recover(); recovered != runnerBinarySHA256Env+" must be a lowercase SHA-256" {
					t.Errorf("wrong or missing digest refusal: %v", recovered)
				}
			}()

			_ = ProbePrelude(target, "https://fixture.invalid/forge-ci")

			t.Error("invalid digest returned a rendered prelude")
		})
	}
}

func TestProbePrelude_ChecksBytesBeforeExecutingFixture(t *testing.T) {
	const binary = "#!/bin/sh\nprintf '%s\\n' \"$*\" >> invoked\nprintf 'artifacts.yml present\\n'\n"

	for _, match := range []bool{true, false} {
		t.Run(fmt.Sprintf("matching=%t", match), func(t *testing.T) {
			root := t.TempDir()
			tools := filepath.Join(root, "tools")
			require.NoError(t, os.Mkdir(tools, 0o700))

			fixture := filepath.Join(root, "fixture-binary")
			require.NoError(t, os.WriteFile(fixture, []byte(binary), 0o600))
			// The only fetch command is an owned copy stub. CA output is also
			// redirected into this fixture tree; no shared /tmp or host trust is changed.
			require.NoError(t, os.WriteFile(filepath.Join(tools, "curl"), []byte("#!/bin/sh\n[ \"$1\" = -fsSL ] && [ \"$2\" = -o ] && [ \"$3\" = forge-ci ] && [ \"$4\" = https://fixture.invalid/forge-ci ] || exit 90\ncp \"$FIXTURE_BINARY\" forge-ci\n"), 0o700)) //nolint:gosec // executable fixture, not a downloaded tool.
			caFile := filepath.Join(root, "source-ca.crt")
			require.NoError(t, os.WriteFile(caFile, independentCAPEM(t), 0o600))
			target := Target{CAFile: caFile, accepted: true}

			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(binary)))
			if !match {
				digest = strings.Repeat("0", 64)
			}

			t.Setenv(runnerBinarySHA256Env, digest)

			script := strings.ReplaceAll(ProbePrelude(target, "https://fixture.invalid/forge-ci"), LabCAPath, filepath.Join(root, "lab-ca.crt")) + "\nrun_product doctor\n"

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-eu", "-c", script) //nolint:gosec // bounded fixture script, owned curl substitute and closed environment.
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + tools + ":" + testenv.GNUPath(t, "sha256sum") + ":/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root, "FIXTURE_BINARY=" + fixture}
			out, err := cmd.CombinedOutput()

			require.NoError(t, ctx.Err())

			invoked, readErr := os.ReadFile(filepath.Join(root, "invoked"))

			if match {
				require.NoError(t, err, "%s", out)
				require.NoError(t, readErr)
				require.Equal(t, "--version\ndoctor\n", string(invoked))
			} else {
				require.Error(t, err)
				require.ErrorIs(t, readErr, os.ErrNotExist)
			}
		})
	}
}
