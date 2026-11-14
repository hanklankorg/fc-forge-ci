// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/require"
)

type fetchCargo struct {
	calls    []appbuild.CargoRunInput
	failures int
	// said is what a failing fetch writes to stderr; cargoOffline when empty.
	said string
}

// cargoOffline is how cargo ends when it cannot reach the registry.
const cargoOffline = "error: failed to get `dep` as a dependency of package `app v0.1.0`\n\nCaused by:\n  [6] Could not resolve hostname (Could not resolve host: index.crates.io)\n"

// cargoRefusal is how cargo ends when a crate is not the one Cargo.lock names.
const cargoRefusal = "error: checksum for `dep v1.2.3` changed between lock files\n\nthis could be indicative of a few possible errors\n"

func (f *fetchCargo) Run(_ context.Context, in appbuild.CargoRunInput) error {
	f.calls = append(f.calls, in)
	if len(f.calls) <= f.failures {
		said := f.said
		if said == "" {
			said = cargoOffline
		}

		_, _ = io.WriteString(in.Stderr, said)

		return fmt.Errorf("cargo fetch: exit status 101: %w", errs.ErrValidation)
	}

	return nil
}

// Only the network is worth another attempt, and only it is an unavailable
// dependency. A crate cargo refuses is refused again, so it is returned from
// the first attempt as cargo's own failure.
func TestCargoFetchRetriesOnlyATransportFailure(t *testing.T) {
	t.Parallel()

	offline := &fetchCargo{failures: 9}
	err := appbuild.CargoFetchWithRetry(t.Context(), offline, io.Discard, io.Discard, appbuild.CargoFetchInput{Attempts: 3})
	require.ErrorIs(t, err, errs.ErrDependencyUnavailable)
	require.NotErrorIs(t, err, errs.ErrValidation)
	require.Len(t, offline.calls, 3)

	refused := &fetchCargo{failures: 9, said: cargoRefusal}
	err = appbuild.CargoFetchWithRetry(t.Context(), refused, io.Discard, io.Discard, appbuild.CargoFetchInput{Attempts: 3})
	require.ErrorIs(t, err, errs.ErrValidation)
	require.NotErrorIs(t, err, errs.ErrDependencyUnavailable)
	require.Len(t, refused.calls, 1)
}

func TestCargoFetchBoundedReads(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		name                      string
		attempts, failures, calls int
		failed                    bool
	}{
		{"once", 1, 0, 1, false}, {"transient", 3, 2, 3, false}, {"exhausted", 2, 3, 2, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			tool := &fetchCargo{failures: scenario.failures}
			err := appbuild.CargoFetchWithRetry(t.Context(), tool, io.Discard, io.Discard, appbuild.CargoFetchInput{Dir: "module", Attempts: scenario.attempts})
			require.Equal(t, scenario.failed, err != nil)
			require.Len(t, tool.calls, scenario.calls)

			for _, call := range tool.calls {
				require.Equal(t, "module", call.Dir)
				require.Equal(t, []string{"fetch", "--locked"}, call.Args)
			}
		})
	}
}

func TestCargoFetchRefusesInvalidRetryAndCancellationBeforeExecution(t *testing.T) {
	t.Parallel()

	for _, in := range []appbuild.CargoFetchInput{{Attempts: 0}, {Attempts: 11}, {Attempts: 1, RetryDelay: -1}, {Attempts: 1, RetryDelay: time.Minute + 1}} {
		tool := &fetchCargo{}
		require.Error(t, appbuild.CargoFetchWithRetry(t.Context(), tool, io.Discard, io.Discard, in))
		require.Empty(t, tool.calls)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tool := &fetchCargo{}
	require.ErrorIs(t, appbuild.CargoFetchWithRetry(ctx, tool, io.Discard, io.Discard, appbuild.CargoFetchInput{Attempts: 3}), context.Canceled)
	require.Empty(t, tool.calls)
}
