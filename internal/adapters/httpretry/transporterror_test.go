// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
)

func TestSafeError_ClassifiesWithoutRenderingPrivateCauses(t *testing.T) {
	t.Parallel()

	const private = "PRIVATE-transport-canary"
	for _, test := range []struct {
		name, category string
		cause          error
	}{
		{"canceled", "request canceled", context.Canceled},
		{"deadline", "request deadline exceeded", context.DeadlineExceeded},
		{"timeout", "network timeout", &net.DNSError{Name: private, Server: private, Err: private, IsTimeout: true}},
		{"socket-timeout", "network timeout", &net.OpError{Op: private, Net: private, Err: syscall.ETIMEDOUT}},
		{"dns", "DNS lookup failed", &net.DNSError{Name: private, Server: private, Err: private}},
		{"certificate", "TLS certificate verification failed", &tls.CertificateVerificationError{Err: errors.New(private)}},
		{"reset", "connection reset", syscall.ECONNRESET},
		{"refused", "connection refused", syscall.ECONNREFUSED},
		{"pipe", "broken connection", syscall.EPIPE},
		{"eof", "connection closed before response completed", io.EOF},
		{"truncated", "connection closed before response completed", io.ErrUnexpectedEOF},
		{"unknown", "unclassified network failure", errors.New(private)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			wrapped := &url.Error{Op: private, URL: "https://" + private + "/?token=" + private,
				Err: fmt.Errorf("%s: %w", private, test.cause)}

			failure := httpretry.SafeError(wrapped)
			if !errors.Is(failure, test.cause) {
				t.Fatal("transport cause identity was lost")
			}

			if value, ok := errors.AsType[*url.Error](failure); !ok || value != wrapped {
				t.Fatal("typed transport cause was lost")
			}

			for _, format := range []string{"%s", "%v", "%+v", "%#v"} {
				output := fmt.Sprintf(format, failure)
				if strings.Contains(output, private) || output != "HTTP transport failed: "+test.category {
					t.Fatalf("unsafe or unclassified diagnostic for %s: %s", format, output)
				}
			}
		})
	}
}

func TestSafeError_NilStaysNil(t *testing.T) {
	t.Parallel()

	if httpretry.SafeError(nil) != nil {
		t.Fatal("nil error became a failure")
	}
}
