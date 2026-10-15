// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry_test

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/require"
)

// A hint within MaxDelay is waited out exactly. One beyond it, however large
// the number, ends the request at once: no wait, no second request, and no
// overflow on the way to a Duration.
func TestRetryAfter_BoundsBeforeDurationConversion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value   string
		want    time.Duration
		tooLong bool
	}{
		{value: "0"}, {value: "1", want: time.Second}, {value: "30", want: 30 * time.Second},
		{value: "31", tooLong: true}, {value: "9223372037", tooLong: true},
		{value: "9223372036854775807", tooLong: true}, {value: "18446744073709551616", tooLong: true},
	} {
		clock := newFakeClock()
		calls := 0
		transport := httpretry.NewTransport(httpretry.Config{Clock: clock, MaxAttempts: 2, MaxDelay: 30 * time.Second, Inner: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++

			status := http.StatusOK
			if calls == 1 {
				status = http.StatusTooManyRequests
			}

			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{tc.value}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		})})
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://retry.invalid/file", nil)
		require.NoError(t, err)

		resp, err := transport.RoundTrip(req)
		if tc.tooLong {
			require.ErrorIs(t, err, errs.ErrRateLimited, tc.value)
			require.Nil(t, resp, tc.value)
			require.Equal(t, 1, calls, tc.value)
			require.Empty(t, clock.Waited(), tc.value)

			continue
		}

		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, []time.Duration{tc.want}, clock.Waited(), tc.value)
	}
}

func TestRetryDiagnostics_DoNotExposeRequestSecrets(t *testing.T) {
	var log bytes.Buffer

	old := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	const secret = "request-secret-fixture"
	for _, failure := range []bool{false, true} {
		cause := fmt.Errorf("reflected %s: %w", secret, errs.ErrPermissionDenied)
		transport := httpretry.NewTransport(httpretry.Config{Clock: newFakeClock(), MaxAttempts: 2, Inner: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if failure {
				return nil, cause
			}

			return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: secret, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(secret)), Request: req}, nil
		})})
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://user:"+secret+"@retry.invalid/"+secret+"?token="+secret, nil)
		require.NoError(t, err)

		response, err := transport.RoundTrip(req)
		if response != nil {
			require.NoError(t, response.Body.Close())
		}

		require.Error(t, err)

		if failure {
			require.ErrorIs(t, err, cause)
		} else {
			require.ErrorIs(t, err, errs.ErrDependencyUnavailable)
		}

		require.NotContains(t, err.Error(), secret)
		require.NotContains(t, log.String(), secret)
		require.Contains(t, log.String(), "retry.invalid")
	}

	// A mutation is sent once and never retried; its transport failure is held
	// to the same fixed text.
	cause := fmt.Errorf("reflected %s: %w", secret, errs.ErrPermissionDenied)
	transport := httpretry.NewTransport(httpretry.Config{Inner: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, cause
	})})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://retry.invalid/"+secret, strings.NewReader("{}"))
	require.NoError(t, err)

	_, err = transport.RoundTrip(req) //nolint:bodyclose // the transport failed; there is no response.
	require.ErrorIs(t, err, cause)
	require.NotContains(t, err.Error(), secret)
}
