// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
)

func get(ctx context.Context, t *testing.T, client *http.Client, url string) (int, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	got, err := io.ReadAll(resp.Body)

	return len(got), err
}

// The cap reads and classifies as http.Client.Timeout did, so callers and
// exit codes that know that error keep working.
func TestNewClient_CapsAnOrdinaryRequestLikeClientTimeout(t *testing.T) {
	t.Parallel()

	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { <-req.Context().Done() }))
	t.Cleanup(hung.Close)

	slow := httptest.NewServer(trickle(10, 40*time.Millisecond, false))
	t.Cleanup(slow.Close)

	client := httpretry.NewClient(100*time.Millisecond, nil)
	if client.Timeout != 0 {
		t.Fatalf("client Timeout = %v, want 0: the transport carries the cap", client.Timeout)
	}

	for name, tc := range map[string]struct{ url, phase string }{
		"headers": {hung.URL, "Client.Timeout exceeded while awaiting headers"},
		"body":    {slow.URL, "Client.Timeout exceeded while reading body"},
	} {
		_, err := get(context.Background(), t, client, tc.url)

		var netErr net.Error
		if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &netErr) || !netErr.Timeout() || !strings.Contains(err.Error(), tc.phase) {
			t.Errorf("%s: err = %v, want a timeout naming %q", name, err, tc.phase)
		}
	}
}

func TestNewClient_LeavesACancellationAlone(t *testing.T) {
	t.Parallel()

	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { <-req.Context().Done() }))
	t.Cleanup(hung.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := get(ctx, t, httpretry.NewClient(time.Hour, nil), hung.URL)
	if err == nil || strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatalf("err = %v, want the caller's own deadline, not the cap", err)
	}
}

// Retries run inside one capped request, as under http.Client.Timeout.
func TestNewClient_CapsTheWholeRetryLoop(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts.Add(1)
		<-req.Context().Done()

		return nil, req.Context().Err()
	})
	client := httpretry.NewClient(50*time.Millisecond, httpretry.NewTransport(httpretry.Config{Inner: inner, MaxAttempts: 2, MaxDelay: 5 * time.Second}))

	_, err := get(t.Context(), t, client, testURL)
	if !errors.Is(err, context.DeadlineExceeded) || attempts.Load() != 1 {
		t.Fatalf("attempts=%d err=%v, want one attempt ended by the cap", attempts.Load(), err)
	}
}

// A request under a Transfer is bounded by progress by the client itself: no
// copy of the client, no wrapped body.
func TestNewClient_ATransferOutlivesTheCapWhileItMoves(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(trickle(10, 40*time.Millisecond, false))
	t.Cleanup(srv.Close)

	client := httpretry.NewClient(150*time.Millisecond, nil)

	if _, err := get(context.Background(), t, client, srv.URL); err == nil {
		t.Fatal("a 400ms body under a 150ms cap succeeded; the test no longer shows the fault")
	}

	ctx, transfer := httpretry.StartTransfer(context.Background(), 150*time.Millisecond)
	defer transfer.Stop()

	if n, err := get(ctx, t, client, srv.URL); err != nil || n != 10 {
		t.Fatalf("transfer read %d bytes, err %v; want all 10", n, err)
	}
}

func TestNewClient_AStalledTransferSaysItStalled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(trickle(1, time.Millisecond, true))
	t.Cleanup(srv.Close)

	ctx, transfer := httpretry.StartTransfer(context.Background(), 100*time.Millisecond)
	defer transfer.Stop()

	_, err := get(ctx, t, httpretry.NewClient(time.Hour, nil), srv.URL)
	if !errors.Is(err, httpretry.ErrStalled) {
		t.Fatalf("err = %v, want ErrStalled without the caller translating it", err)
	}
}

// Deriving the 500-retry policy keeps the bound outside the retry loop.
func TestWithInternalServerErrorRetries_KeepsTheBound(t *testing.T) {
	t.Parallel()

	client := httpretry.NewClient(time.Minute, httpretry.NewTransport(httpretry.Config{}))

	derived, ok := httpretry.WithInternalServerErrorRetries(client.Transport).(*httpretry.BoundedTransport)
	if !ok || derived.Timeout() != time.Minute {
		t.Fatalf("derived transport = %T, want a BoundedTransport with the same cap", derived)
	}

	if _, ok := derived.Inner().(*httpretry.Transport); !ok {
		t.Fatalf("derived inner = %T, want the retry transport", derived.Inner())
	}
}
