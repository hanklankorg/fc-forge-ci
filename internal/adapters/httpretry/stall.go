// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ErrStalled marks a body transfer that moved no bytes for its idle window.
var ErrStalled = fmt.Errorf("transfer stalled: %w", errs.ErrDependencyUnavailable)

// Transfer bounds the requests whose bodies are files by progress rather than
// by wall time.
//
// A client's timeout caps a whole request, body included, which suits small
// JSON answers but cuts off any body larger than the link moves in that time:
// a gigabyte seldom moves in 30 seconds. A Transfer instead cancels its
// requests only once idle passes with no byte read from the bodies it
// watches, so a large transfer runs as long as it keeps moving and a stalled
// one still ends.
//
// A request made with the context StartTransfer returns is bounded this way
// by any client from NewClient, the API clients included, so an SDK that
// takes the file itself or returns the body needs only that context.
type Transfer struct {
	cause  func() error
	cancel context.CancelCauseFunc
	timer  *time.Timer
	idle   time.Duration
}

// transferKey is the context key a Transfer is stored under.
type transferKey struct{}

// StartTransfer returns the context to make the transfer's requests with and
// the Transfer that cancels it after idle without progress. The wait for the
// response headers counts as idle time. Call Stop when the transfer is done.
func StartTransfer(ctx context.Context, idle time.Duration) (context.Context, *Transfer) {
	ctx, cancel := context.WithCancelCause(ctx)
	t := &Transfer{cause: func() error { return context.Cause(ctx) }, cancel: cancel, idle: idle}
	t.timer = time.AfterFunc(idle, func() {
		cancel(fmt.Errorf("no bytes moved for %s: %w", idle, ErrStalled))
	})

	return context.WithValue(ctx, transferKey{}, t), t
}

// transferFrom returns the Transfer ctx was made for, or nil.
func transferFrom(ctx context.Context) *Transfer {
	t, _ := ctx.Value(transferKey{}).(*Transfer)

	return t
}

// Client returns a copy of client without its whole-request Timeout whose
// transport watches every request and response body it carries. It is for a
// client that does not come from NewClient, such as one a test injects; a
// client from NewClient bounds a transfer's requests by their context alone.
func (t *Transfer) Client(client *http.Client) *http.Client {
	unbounded := *client
	unbounded.Timeout = 0
	unbounded.Transport = watchedTransport{next: client.Transport, t: t}

	return &unbounded
}

// watchedTransport restarts a Transfer's idle window on each byte its
// requests send or its responses deliver.
type watchedTransport struct {
	next http.RoundTripper
	t    *Transfer
}

func (w watchedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	next := w.next
	if next == nil {
		next = http.DefaultTransport
	}

	return w.t.roundTrip(next, req)
}

// progressBody is a watched body that still closes its source.
type progressBody struct {
	io.Reader
	io.Closer
}

// Reader wraps a request or response body so each read that moves bytes
// restarts the idle window, and a read the stall ended reports the stall.
func (t *Transfer) Reader(r io.Reader) io.Reader {
	return &progressReader{r: r, t: t}
}

// Err returns the stall itself in place of the bare cancellation it caused,
// and err unchanged otherwise.
func (t *Transfer) Err(err error) error {
	if err == nil {
		return nil
	}

	if cause := t.cause(); errors.Is(cause, ErrStalled) {
		return cause
	}

	return err
}

// Stop releases the Transfer; its context is cancelled.
func (t *Transfer) Stop() {
	t.timer.Stop()
	t.cancel(context.Canceled)
}

// roundTrip sends req through next with its request and response bodies
// watched, and reports a stall as the stall.
func (t *Transfer) roundTrip(next http.RoundTripper, req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody {
		watched := req.Clone(req.Context())
		watched.Body = progressBody{Reader: t.Reader(req.Body), Closer: req.Body}

		if req.GetBody != nil {
			watched.GetBody = func() (io.ReadCloser, error) {
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}

				return progressBody{Reader: t.Reader(body), Closer: body}, nil
			}
		}

		req = watched
	}

	resp, err := next.RoundTrip(req)
	if err != nil {
		return nil, t.Err(err)
	}

	resp.Body = progressBody{Reader: t.Reader(resp.Body), Closer: resp.Body}

	return resp, nil
}

type progressReader struct {
	r io.Reader
	t *Transfer
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.t.timer.Reset(p.t.idle)
	}

	if err != nil && !errors.Is(err, io.EOF) {
		err = p.t.Err(err)
	}

	return n, err //nolint:wrapcheck // a reader passes its source's io.EOF through unwrapped.
}
