// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestParseClientTimeout_FallsBackToTheDefaultOnAnyUnusableValue(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]time.Duration{
		"":      httpretry.DefaultClientTimeout,
		"45s":   45 * time.Second,
		" 2m ":  2 * time.Minute,
		"soon":  httpretry.DefaultClientTimeout,
		"0s":    httpretry.DefaultClientTimeout,
		"-5s":   httpretry.DefaultClientTimeout,
		"   ":   httpretry.DefaultClientTimeout,
		"1500":  httpretry.DefaultClientTimeout,
		"250ms": 250 * time.Millisecond,
	} {
		if got := httpretry.ParseClientTimeout(raw); got != want {
			t.Errorf("ParseClientTimeout(%q) = %v, want %v", raw, got, want)
		}
	}
}

// TestClientTimeout_ReadsTheEnvironment is the one wiring check left on the
// process environment; the rules are tabled against the pure parser above.
func TestClientTimeout_ReadsTheEnvironment(t *testing.T) {
	// Not parallel: t.Setenv.
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", "45s")

	if got := httpretry.ClientTimeout(); got != 45*time.Second {
		t.Errorf("ClientTimeout() = %v, want the 45s override", got)
	}
}

func TestRetryAfterBeyondClientDeadlinePreservesServerClass(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			var attempts atomic.Int32

			inner := serveInMemory(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(status)
			})
			client := &http.Client{
				Timeout:   50 * time.Millisecond,
				Transport: httpretry.NewTransport(httpretry.Config{Inner: inner, MaxAttempts: 2, MaxDelay: 5 * time.Second}),
			}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, testURL, nil)
			if err != nil {
				t.Fatal(err)
			}

			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}

			want := errs.ErrRateLimited
			if status == http.StatusServiceUnavailable {
				want = errs.ErrDependencyUnavailable
			}

			if !errors.Is(err, want) || errors.Is(err, context.DeadlineExceeded) || attempts.Load() != 1 {
				t.Fatalf("attempts=%d err=%v, want one attempt and %v", attempts.Load(), err, want)
			}
		})
	}
}

// A timeout during an actual request must still preserve deadline identity.
func TestClientTimeout_CapsTheWholeRetryLoop(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts.Add(1)
		<-req.Context().Done()

		return nil, req.Context().Err()
	})

	client := &http.Client{
		Timeout:   httpretry.ParseClientTimeout("50ms"),
		Transport: httpretry.NewTransport(httpretry.Config{Inner: inner, MaxAttempts: 2, MaxDelay: 5 * time.Second}),
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, testURL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the client deadline to end the retry wait", err)
	}

	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1: the wait before the second was cut short", got)
	}
}
