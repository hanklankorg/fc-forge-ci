// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// trickle answers with rounds bytes, one per gap, then stalls forever when
// stall is set.
func trickle(rounds int, gap time.Duration, stall bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		flusher, _ := w.(http.Flusher)

		for range rounds {
			_, _ = w.Write([]byte{'x'})

			flusher.Flush()

			select {
			case <-req.Context().Done():
				return
			case <-time.After(gap):
			}
		}

		if stall {
			<-req.Context().Done()
		}
	})
}

func fetch(ctx context.Context, client *http.Client, url string, transfer *httpretry.Transfer) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	if transfer != nil {
		client = transfer.Client(client)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	var body io.Reader = resp.Body
	if transfer != nil {
		body = transfer.Reader(body)
	}

	got, err := io.ReadAll(body)

	return len(got), err
}

func TestTransfer_ABodyThatKeepsMovingOutlivesTheClientTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(trickle(10, 40*time.Millisecond, false))
	t.Cleanup(srv.Close)

	client := &http.Client{Timeout: 150 * time.Millisecond}

	// The whole-request Timeout alone cuts the 400ms body off: the fault seen
	// live, a large artifact entry cut off after 30 seconds.
	if _, err := fetch(context.Background(), client, srv.URL, nil); err == nil {
		t.Fatal("a 400ms body under a 150ms client Timeout succeeded; the test no longer shows the fault")
	}

	ctx, transfer := httpretry.StartTransfer(context.Background(), 150*time.Millisecond)
	defer transfer.Stop()

	n, err := fetch(ctx, client, srv.URL, transfer)
	if err != nil || n != 10 {
		t.Fatalf("transfer read %d bytes, err %v; want all 10", n, err)
	}
}

func TestTransfer_AStalledBodySaysItStalled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(trickle(1, time.Millisecond, true))
	t.Cleanup(srv.Close)

	ctx, transfer := httpretry.StartTransfer(context.Background(), 100*time.Millisecond)
	defer transfer.Stop()

	_, err := fetch(ctx, &http.Client{}, srv.URL, transfer)

	err = transfer.Err(err)
	if !errors.Is(err, httpretry.ErrStalled) || !errors.Is(err, errs.ErrDependencyUnavailable) {
		t.Fatalf("stalled body error = %v, want ErrStalled and ErrDependencyUnavailable", err)
	}
}

func TestTransfer_ErrLeavesOtherErrorsAlone(t *testing.T) {
	t.Parallel()

	_, transfer := httpretry.StartTransfer(context.Background(), time.Hour)
	defer transfer.Stop()

	if got := transfer.Err(io.ErrUnexpectedEOF); !errors.Is(got, io.ErrUnexpectedEOF) {
		t.Fatalf("Err = %v, want the error it was given", got)
	}

	if transfer.Err(nil) != nil {
		t.Fatal("Err(nil) is not nil")
	}
}

func TestTransfer_ClientLeavesTheGivenClientBounded(t *testing.T) {
	t.Parallel()

	_, transfer := httpretry.StartTransfer(context.Background(), time.Hour)
	defer transfer.Stop()

	client := &http.Client{Timeout: time.Second}
	if transfer.Client(client).Timeout != 0 || client.Timeout != time.Second {
		t.Fatal("Client must return an unbounded copy and leave the API client's Timeout alone")
	}
}
