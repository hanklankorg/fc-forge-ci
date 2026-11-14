// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"context"
	"fmt"
	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/require"
	"io"
	"testing"
	"time"
)

type downloadGoTool struct {
	calls     int
	succeedAt int
	cancel    context.CancelFunc
	// said is what a failing download writes to stderr; goOffline when empty.
	said string
}

// goOffline is how go ends when it cannot reach the module proxy.
const goOffline = "go: example.org/dep@v1.2.3: Get \"https://proxy.example/example.org/dep/@v/v1.2.3.mod\": dial tcp: lookup proxy.example: no such host\n"

// goRefusal is how go ends when a module is not the one go.sum names.
const goRefusal = "verifying example.org/dep@v1.2.3: checksum mismatch\n\tdownloaded: h1:aaaa\n\tgo.sum:     h1:bbbb\n\nSECURITY ERROR\n"

func (f *downloadGoTool) Run(_ context.Context, in appbuild.GoRunInput) error {
	f.calls++
	if f.cancel != nil {
		f.cancel()
	}

	if f.calls == f.succeedAt {
		return nil
	}

	said := f.said
	if said == "" {
		said = goOffline
	}

	_, _ = io.WriteString(in.Stderr, said)

	return fmt.Errorf("go mod: exit status 1: %w", errs.ErrValidation)
}

// Only the network is worth another attempt, and only it is an unavailable
// dependency. A module go refuses is refused again, so it is returned from
// the first attempt as go's own failure.
func TestGoDownloadRetriesOnlyATransportFailure(t *testing.T) {
	t.Parallel()

	offline := &downloadGoTool{}
	err := appbuild.GoDownloadWithRetry(t.Context(), offline, io.Discard, io.Discard, appbuild.GoDownloadInput{Attempts: 3})
	require.ErrorIs(t, err, errs.ErrDependencyUnavailable)
	require.NotErrorIs(t, err, errs.ErrValidation)
	require.Equal(t, 3, offline.calls)

	refused := &downloadGoTool{said: goRefusal}
	err = appbuild.GoDownloadWithRetry(t.Context(), refused, io.Discard, io.Discard, appbuild.GoDownloadInput{Attempts: 3})
	require.ErrorIs(t, err, errs.ErrValidation)
	require.NotErrorIs(t, err, errs.ErrDependencyUnavailable)
	require.Equal(t, 1, refused.calls)
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
