// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"net/http"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
)

// TestHTTPClient_DefaultIsBounded guards the production path: when no
// client is injected (forgejo.New()), the Gitea SDK must NOT fall back
// to its unbounded &http.Client{} default. An unbounded client lets a
// slow/hung Forgejo server stall a release step forever, so the bound
// is a hard requirement, not a nicety. The bound lives in the transport,
// not in the client's Timeout, so a file body can be bounded by progress.
func TestHTTPClient_DefaultIsBounded(t *testing.T) {
	t.Parallel()

	client := New().httpClient()
	if client.Timeout != 0 {
		t.Errorf("default forgejo client Timeout = %v, want 0: a whole-request Timeout cuts a moving file body off", client.Timeout)
	}

	bounded, ok := client.Transport.(*httpretry.BoundedTransport)
	if !ok {
		t.Fatalf("default forgejo client transport = %T, want *httpretry.BoundedTransport", client.Transport)
	}

	// The timeout is the shared one, compared against the same process
	// environment the client read, so FORGE_CI_HTTP_TIMEOUT being set or
	// not on the machine running the suite cannot change the outcome.
	if want := httpretry.ClientTimeout(); bounded.Timeout() != want || want <= 0 {
		t.Errorf("default forgejo client timeout = %v, want the shared httpretry timeout %v", bounded.Timeout(), want)
	}

	// Not merely a non-nil transport: http.DefaultTransport is non-nil too,
	// and a client built on it would lose every retry and backoff bound the
	// github and gitlab adapters apply.
	if _, ok := bounded.Inner().(*httpretry.Transport); !ok {
		t.Errorf("default forgejo client inner transport = %T, want *httpretry.Transport", bounded.Inner())
	}
}

// TestHTTPClient_HonorsInjection keeps the test seam working: an injected
// client (httptest-backed in tests) is used verbatim, not replaced.
func TestHTTPClient_HonorsInjection(t *testing.T) {
	t.Parallel()

	// A client that shares nothing with the default, so using the default
	// instead cannot look like honouring the injection.
	injected := &http.Client{Timeout: time.Minute}
	p := &Provider{HTTPClient: injected}

	if p.httpClient() != injected {
		t.Error("injected HTTPClient was not used")
	}
}
