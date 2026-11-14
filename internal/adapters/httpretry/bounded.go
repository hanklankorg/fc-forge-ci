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
)

// NewClient returns a client whose transport bounds each request: by timeout
// for an ordinary request, its response body included, and by progress for a
// request whose context carries a Transfer. The client's own Timeout is zero,
// since a whole-request Timeout would cut a file body off however well it
// moves. A nil inner is http.DefaultTransport.
//
// A retry transport belongs inside, as inner: the timeout then caps the whole
// retry loop of one request, as http.Client.Timeout did.
func NewClient(timeout time.Duration, inner http.RoundTripper) *http.Client {
	return &http.Client{Transport: NewBoundedTransport(timeout, inner)}
}

// BoundedTransport is the transport NewClient installs.
type BoundedTransport struct {
	next    http.RoundTripper
	timeout time.Duration
}

// NewBoundedTransport wraps inner, or http.DefaultTransport when nil, in the
// bounds NewClient describes. A timeout of zero or less sets no time cap.
func NewBoundedTransport(timeout time.Duration, inner http.RoundTripper) *BoundedTransport {
	if inner == nil {
		inner = http.DefaultTransport
	}

	return &BoundedTransport{next: inner, timeout: timeout}
}

// Timeout reports the cap on an ordinary request.
func (b *BoundedTransport) Timeout() time.Duration { return b.timeout }

// Inner returns the transport the bounded requests go through.
func (b *BoundedTransport) Inner() http.RoundTripper { return b.next }

// errCapReached is the cause a capped request's context ends with, which tells
// the cap firing apart from the caller cancelling. It reads as, and is,
// context.DeadlineExceeded, as the cause net/http's own timeout leaves.
var errCapReached = fmt.Errorf("%w", context.DeadlineExceeded)

// RoundTrip implements http.RoundTripper.
func (b *BoundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if transfer := transferFrom(req.Context()); transfer != nil {
		return transfer.roundTrip(b.next, req)
	}

	if b.timeout <= 0 {
		return b.next.RoundTrip(req) //nolint:wrapcheck // a transport passes its inner transport's error through.
	}

	ctx, cancel := context.WithTimeoutCause(req.Context(), b.timeout, errCapReached)
	capped := func() bool { return errors.Is(context.Cause(ctx), errCapReached) }

	resp, err := b.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		if capped() {
			err = &capError{err: err, phase: "awaiting headers"}
		}

		cancel()

		return nil, err
	}

	resp.Body = &cappedBody{ReadCloser: resp.Body, capped: capped, cancel: cancel}

	return resp, nil
}

// cappedBody keeps a capped request's deadline running while its body is read,
// and releases it when the body is closed.
type cappedBody struct {
	io.ReadCloser

	capped func() bool
	cancel context.CancelFunc
}

func (c *cappedBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && c.capped() {
		err = &capError{err: err, phase: "reading body"}
	}

	return n, err //nolint:wrapcheck // a body passes its source's io.EOF through unwrapped.
}

func (c *cappedBody) Close() error {
	defer c.cancel()

	return c.ReadCloser.Close() //nolint:wrapcheck // a body passes its source's close error through.
}

// capError is the error of a request its timeout ended. It reads and
// classifies as net/http's own Client.Timeout error does: a timeout that is
// context.DeadlineExceeded.
type capError struct {
	err   error
	phase string
}

func (e *capError) Error() string {
	return e.err.Error() + " (Client.Timeout exceeded while " + e.phase + ")"
}

func (e *capError) Unwrap() error { return e.err }

// Timeout marks the error as a timeout for net.Error and url.Error.
func (e *capError) Timeout() bool { return true }

// Temporary matches net/http's timeout error.
func (e *capError) Temporary() bool { return true }

// Is makes the error context.DeadlineExceeded whatever the inner error.
func (e *capError) Is(target error) bool { return target == context.DeadlineExceeded }
