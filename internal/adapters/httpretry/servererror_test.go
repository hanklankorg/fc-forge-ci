// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry_test

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestInternalServerErrorRetryPolicy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, method string
		optIn        bool
		replay       bool
		failures     int
		attempts     int
		exhausted    bool
		status       int // the failure status; 0 means HTTP 500
	}{
		{"default_is_terminal", http.MethodGet, false, false, 10, 1, false, 0},
		{"read_recovers", http.MethodGet, true, false, 2, 3, false, 0},
		{"head_recovers", http.MethodHead, true, false, 2, 3, false, 0},
		{"read_exhausts", http.MethodGet, true, false, 10, 3, true, 0},
		{"post_once", http.MethodPost, true, false, 10, 1, false, 0},
		{"put_once", http.MethodPut, true, false, 10, 1, false, 0},
		{"patch_once", http.MethodPatch, true, false, 10, 1, false, 0},
		{"delete_once", http.MethodDelete, true, false, 10, 1, false, 0},
		{"marked_patch_recovers", http.MethodPatch, true, true, 2, 3, false, 0},
		{"marked_patch_exhausts", http.MethodPatch, true, true, 10, 3, true, 0},
		{"marked_patch_default_is_terminal", http.MethodPatch, false, true, 10, 1, false, 0},
		{"marked_patch_unavailable_once", http.MethodPatch, true, true, 10, 1, false, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			inner := serveInMemory(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls <= tc.failures {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(cmp.Or(tc.status, http.StatusInternalServerError))
				}
			})
			clock := newFakeClock()
			base := httpretry.NewTransport(httpretry.Config{Inner: inner, Clock: clock, MaxAttempts: 3, MaxDelay: time.Second, MaxCumulativeDelay: 2 * time.Second})

			var transport http.RoundTripper = base
			if tc.optIn {
				transport = httpretry.WithInternalServerErrorRetries(base)
			}

			ctx := t.Context()
			if tc.replay {
				ctx = httpretry.ReplayRefusedMutation(ctx)
			}

			req, err := http.NewRequestWithContext(ctx, tc.method, testURL, nil)
			if err != nil {
				t.Fatal(err)
			}

			resp, err := transport.RoundTrip(req)
			if resp != nil {
				defer func() { _ = resp.Body.Close() }()
			}

			if tc.exhausted {
				if !errors.Is(err, errs.ErrDependencyUnavailable) || resp != nil {
					t.Fatalf("expected exhausted dependency failure, got response=%v error=%v", resp, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}

			if calls != tc.attempts || len(clock.Waited()) != tc.attempts-1 {
				t.Fatalf("attempts=%d waits=%v, expected %d attempts", calls, clock.Waited(), tc.attempts)
			}

			// Deriving the artifact policy must not alter other users of the
			// original transport, or wrap its retry loop inside another one.
			calls = 0

			originalResponse, originalErr := base.RoundTrip(req)
			if originalErr != nil {
				t.Fatal(originalErr)
			}
			defer func() { _ = originalResponse.Body.Close() }()

			if calls != 1 || originalResponse.StatusCode != cmp.Or(tc.status, http.StatusInternalServerError) {
				t.Fatalf("original transport's policy changed: calls=%d status=%d", calls, originalResponse.StatusCode)
			}
		})
	}
}

func TestInternalServerErrorRetryHonoursCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	clock := newFakeClock()
	calls := 0
	inner := serveInMemory(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	})
	transport := httpretry.WithInternalServerErrorRetries(httpretry.NewTransport(httpretry.Config{Inner: inner, Clock: clock}))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := transport.RoundTrip(req)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	if !errors.Is(err, context.Canceled) || resp != nil || calls != 1 || len(clock.Waited()) != 0 {
		t.Fatalf("cancellation must stop retries: response=%v error=%v calls=%d waits=%v", resp, err, calls, clock.Waited())
	}
}
