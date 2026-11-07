// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
)

type transportError struct{ cause error }

const networkTimeoutCategory = "network timeout"

func (e transportError) Error() string {
	return "HTTP transport failed: " + transportFailureCategory(e.cause)
}

func (e transportError) Unwrap() error { return e.cause }

// Alternate formatting must not render the wrapped URL, addresses or private
// cause text. Callers can still inspect the cause with errors.Is/errors.As.
func (e transportError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, e.Error())
}

// Classify identities and typed fields only, never substrings of Error(). A
// transport error may carry a signed URL, credentials or a private hostname.
func transportFailureCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "request deadline exceeded"
	case errors.Is(err, syscall.ETIMEDOUT):
		return networkTimeoutCategory
	}

	if failure, ok := errors.AsType[*net.DNSError](err); ok {
		if failure.Timeout() {
			return networkTimeoutCategory
		}

		return "DNS lookup failed"
	}

	if failure, ok := errors.AsType[net.Error](err); ok && failure.Timeout() {
		return networkTimeoutCategory
	}

	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return "TLS certificate verification failed"
	}

	switch {
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.EPIPE):
		return "broken connection"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection closed before response completed"
	default:
		return "unclassified network failure"
	}
}
