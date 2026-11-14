// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry_test

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The refusal tells the operator where the redirect led, by scheme and host
// only. A redirect's path, query and userinfo are the other server's words
// and may carry a credential, so the guard's own message leaves them out.
func TestSameAuthority_RefusalNamesBothAuthoritiesAndNothingElse(t *testing.T) {
	t.Parallel()

	const reflected = "reflected-secret"

	location := "http://user:" + reflected + "@other.invalid:8080/reflected-path?token=" + reflected

	client := httpretry.SameAuthority(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Location", location)

		return &http.Response{StatusCode: http.StatusFound, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})}, "Forgejo")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://forge.invalid/start?credential=request-secret", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	// net/http wraps the policy's error with the URL it was about to request;
	// the claim here is about the guard's own message inside that.
	var wrapped *url.Error
	if !errors.As(err, &wrapped) || !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("err = %v, want the redirect refused as a validation failure", err)
	}

	refusal := wrapped.Err.Error()
	if !strings.Contains(refusal, "Forgejo redirect changes authority") ||
		!strings.Contains(refusal, "from https://forge.invalid to http://other.invalid:8080") {
		t.Fatalf("refusal = %q, want the fixed phrase and both authorities", refusal)
	}

	for _, private := range []string{reflected, "reflected-path", "request-secret", "user", "/start"} {
		if strings.Contains(refusal, private) {
			t.Fatalf("refusal = %q, must not carry %q", refusal, private)
		}
	}
}
