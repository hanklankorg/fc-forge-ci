// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// defaultHTTPClient returns a Client backed by the retry transport. The
// timeout caps total per-request wall time (default 30s, overridable via
// FORGE_CI_HTTP_TIMEOUT for slow links); the transport's
// MaxCumulativeDelay caps in-process sleep separately.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   httpretry.ClientTimeout(),
		Transport: httpretry.NewTransport(httpretry.Config{}),
	}
}

// doRequest applies the same authority boundary to injected and default clients
// without modifying the caller's client or redirect policy.
func doRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = defaultHTTPClient()
	}

	resp, err := httpretry.SameAuthority(client, "GitLab").Do(req)

	return resp, privateRequestError(err)
}

type requestError struct{ cause error }

func (e requestError) Error() string { return "GitLab request failed" }
func (e requestError) Unwrap() error { return e.cause }
func privateRequestError(err error) error {
	if err == nil {
		return nil
	}

	return requestError{cause: err}
}

// maxResponseBytes bounds an API response read into memory; the JSON this
// adapter reads is small, so anything larger is a faulty server.
const maxResponseBytes = 8 << 20

// readResponseBody reads at most maxResponseBytes of an API response.
func readResponseBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, privateRequestError(err)
	}

	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes: %w", maxResponseBytes, errs.ErrMalformedInput)
	}

	return data, nil
}

func responseStatusError(status int) error {
	cls := errs.FromHTTPStatus(status)
	if cls == nil {
		cls = errs.ErrDependencyUnavailable
	}

	return fmt.Errorf("HTTP %d: %w", status, cls)
}

// getJSON does a GET with the provided headers, returns the body bytes.
// Empty PRIVATE-TOKEN is dropped. Caller is responsible for
// unmarshalling.
func getJSON(ctx context.Context, client *http.Client, url string, headers map[string]string) ([]byte, error) {
	if client == nil {
		client = defaultHTTPClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, privateRequestError(err)
	}

	for k, v := range headers {
		if v == "" {
			continue
		}

		req.Header.Set(k, v)
	}

	resp, err := doRequest(client, req)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := readResponseBody(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseStatusError(resp.StatusCode)
	}

	return body, nil
}

// postJSON sends a POST request with the given body and headers,
// classifying non-2xx responses via errs.FromHTTPStatus when possible.
func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body []byte) error {
	return sendJSON(ctx, client, http.MethodPost, url, headers, body)
}

// putJSON is the in-place update (PUT) counterpart of postJSON, used by the
// reconcile publish strategy.
func putJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body []byte) error {
	return sendJSON(ctx, client, http.MethodPut, url, headers, body)
}

// sendJSON performs a body-carrying request (POST/PUT) with the given headers,
// classifying non-2xx responses via errs.FromHTTPStatus when possible.
func sendJSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte) error {
	if client == nil {
		client = defaultHTTPClient()
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return privateRequestError(err)
	}

	for k, v := range headers {
		if v == "" {
			continue
		}

		req.Header.Set(k, v)
	}

	resp, err := doRequest(client, req)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseStatusError(resp.StatusCode)
	}

	return nil
}

// deleteJSON sends a DELETE request, classifying non-2xx responses via
// errs.FromHTTPStatus when possible.
func deleteJSON(ctx context.Context, client *http.Client, url string, headers map[string]string) error {
	if client == nil {
		client = defaultHTTPClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return privateRequestError(err)
	}

	for k, v := range headers {
		if v == "" {
			continue
		}

		req.Header.Set(k, v)
	}

	resp, err := doRequest(client, req)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseStatusError(resp.StatusCode)
	}

	return nil
}
