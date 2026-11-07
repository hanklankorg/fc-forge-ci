// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/stretchr/testify/require"
)

type fetchCargo struct {
	calls    []appbuild.CargoRunInput
	failures int
}

func (f *fetchCargo) Run(_ context.Context, in appbuild.CargoRunInput) error {
	f.calls = append(f.calls, in)
	if len(f.calls) <= f.failures {
		return errors.New("registry unavailable")
	}

	return nil
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
