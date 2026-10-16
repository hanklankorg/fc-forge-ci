// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The raw JSON calls the kit still makes for itself — workflow files and runs,
// repository secrets, package listings, fixture branches — are the ones the
// module's repository lifecycle does not cover. They speak the forge's wire
// shapes directly, never the adapters under test, over the same
// lab-only transport as everything else here.

// discard performs a request and asserts the status, keeping no body.
func discard(ctx context.Context, target Target, method, endpoint string, body any, accept ...int) error {
	_, err := decode(ctx, target, method, endpoint, body, nil, accept...)

	return err
}

// decode performs an authenticated request, checks the status against the
// accepted set, and optionally decodes the body. It returns the status so a
// caller can distinguish "found" from "absent" without a second call.
func decode(ctx context.Context, target Target, method, endpoint string, body, into any, accept ...int) (int, error) { //nolint:cyclop // Shared wire decoder classifies each forge response shape.
	var reader io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode request: %w", err)
		}

		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	authorize(req, target)

	client, err := targetHTTPClient(target, timeoutAPI)
	if err != nil {
		return 0, fmt.Errorf("configure target HTTP trust: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, redact(endpoint), err)
	}

	defer func() { _ = resp.Body.Close() }()

	if !accepted(resp.StatusCode, accept) {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s: %w", method, redact(endpoint), resp.StatusCode, detail, errs.ErrValidation)
	}

	if into != nil && resp.StatusCode != http.StatusNotFound {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w", redact(endpoint), err)
		}
	}

	return resp.StatusCode, nil
}

// fetch performs an authenticated GET and returns the body as it is, for the
// answers that are not JSON: an archive, a log. A body over limit bytes is an
// error and not a truncation, because half an archive reads as a corrupt one.
// errAbsent marks a fetch the forge answered with 404: the resource is not
// there, which a caller may treat as an outcome rather than a failure.
var errAbsent = errors.New("absent")

func fetch(ctx context.Context, target Target, endpoint string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	authorize(req, target)

	client, err := targetHTTPClient(target, timeoutAPI)
	if err != nil {
		return nil, fmt.Errorf("configure target HTTP trust: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", redact(endpoint), err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("GET %s: HTTP %d: %w: %w", redact(endpoint), resp.StatusCode, errAbsent, errs.ErrValidation)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d: %w", redact(endpoint), resp.StatusCode, errs.ErrValidation)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", redact(endpoint), err)
	}

	if int64(len(body)) > limit {
		return nil, fmt.Errorf("GET %s: the body is over %d bytes: %w", redact(endpoint), limit, errs.ErrValidation)
	}

	return body, nil
}

func accepted(status int, accept []int) bool {
	for _, want := range accept {
		if status == want {
			return true
		}
	}

	return false
}

// authorize sets the token header the target's kind reads, spelled by the
// contract module, so a raw call carries the credential exactly as the
// module's SDK calls do.
func authorize(req *http.Request, target Target) {
	req.Header.Set(labcontract.AuthHeader(labcontract.Kind(target.Forge), target.Token))
}

// redact keeps a failed-request message useful without echoing a query string,
// which on these APIs can carry a token.
func redact(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "<unparsable endpoint>"
	}

	parsed.RawQuery = ""

	return parsed.String()
}
