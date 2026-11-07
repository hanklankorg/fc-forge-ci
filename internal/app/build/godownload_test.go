// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"context"
	"errors"
	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/stretchr/testify/require"
	"io"
	"testing"
	"time"
)

type downloadGoTool struct {
	calls     int
	succeedAt int
	cancel    context.CancelFunc
}

func (f *downloadGoTool) Run(_ context.Context, in appbuild.GoRunInput) error {
	f.calls++
	if f.cancel != nil {
		f.cancel()
	}

	if f.calls == f.succeedAt {
		return nil
	}

	return errors.New("dependency read failed")
}

func TestGoDownloadRetryBoundsAndLegacySingleAttempt(t *testing.T) {
	t.Parallel()

	for _, success := range []int{0, 1, 3} {
		tool := &downloadGoTool{succeedAt: success}

		err := appbuild.GoDownloadWithRetry(t.Context(), tool, io.Discard, io.Discard, appbuild.GoDownloadInput{Attempts: 3})
		if success == 0 {
			require.Error(t, err)
			require.Equal(t, 3, tool.calls)
		} else {
			require.NoError(t, err)
			require.Equal(t, success, tool.calls)
		}
	}

	tool := &downloadGoTool{}
	require.Error(t, appbuild.GoDownload(t.Context(), tool, io.Discard, io.Discard, "."))
	require.Equal(t, 1, tool.calls)

	for _, in := range []appbuild.GoDownloadInput{{Attempts: 0}, {Attempts: 11}, {Attempts: 1, RetryDelay: -1}, {Attempts: 1, RetryDelay: 2 * time.Minute}} {
		tool := &downloadGoTool{}
		require.Error(t, appbuild.GoDownloadWithRetry(t.Context(), tool, nil, nil, in))
		require.Zero(t, tool.calls)
	}
}

func TestGoDownloadCancellationStopsWaitingAndAttempts(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	tool := &downloadGoTool{cancel: cancel}
	require.ErrorIs(t, appbuild.GoDownloadWithRetry(ctx, tool, nil, nil, appbuild.GoDownloadInput{Attempts: 3, RetryDelay: time.Minute}), context.Canceled)
	require.Equal(t, 1, tool.calls)
	require.ErrorIs(t, appbuild.GoDownloadWithRetry(ctx, tool, nil, nil, appbuild.GoDownloadInput{Attempts: 3}), context.Canceled)
	require.Equal(t, 1, tool.calls)
}
