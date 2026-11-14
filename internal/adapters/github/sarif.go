// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// UploadSARIF posts a SARIF report to the GitHub Code Scanning API.
// The SARIF body is gzip-then-base64 encoded as the API requires, then
// wrapped in the JSON payload shape {commit_sha, ref, sarif}. The analysis
// category lives inside the SARIF (each run's automationDetails.id), so the
// payload carries no separate tool_name/category.
//
// Skip semantics (return nil): caller-side — empty Token or empty
// SARIF body. This method always attempts the POST; failures propagate.
func (p *Provider) UploadSARIF(ctx context.Context, up provider.SARIFUpload) error {
	// The repository becomes part of the request path, so an empty or
	// malformed one is refused before anything is sent.
	if _, _, err := splitRepo(up.Repository); err != nil {
		return err
	}

	if up.Token == "" {
		return fmt.Errorf("github upload sarif: token is empty: %w", errs.ErrPermissionDenied)
	}

	encoded, err := gzipBase64(up.SARIF)
	if err != nil {
		return fmt.Errorf("github upload sarif: encode: %w", err)
	}

	payload := map[string]string{
		"commit_sha": up.SHA,
		"ref":        up.Ref,
		"sarif":      encoded,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("github upload sarif: marshal: %w", err)
	}

	headers := map[string]string{
		"Authorization": "token " + up.Token, //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
		"Accept":        acceptJSONHeader,    //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
		"Content-Type":  "application/json",
	}

	// Code Scanning answers a burst of uploads with an occasional 502, and
	// one failed a whole runtime-image publication. An upload of the same
	// SARIF for the same commit and category replaces the analysis, so a
	// retry is safe; only an unavailable service is retried, never a refusal.
	delay := sarifUploadDelay
	if p.sarifRetryDelay > 0 {
		delay = p.sarifRetryDelay
	}

	return retry.Run(ctx, nil, sarifUploadAttempts, delay, func() error {
		err := postJSON(ctx, p.HTTPClient, p.restURL("/repos/"+up.Repository+"/code-scanning/sarifs"), headers, body)
		if err != nil && !errors.Is(err, errs.ErrDependencyUnavailable) {
			return retry.Permanent(err)
		}

		return err
	})
}

// sarifUploadAttempts and sarifUploadDelay bound the retry of an unavailable
// Code Scanning API: waits of 5, 10 and 15 seconds, about half a minute.
const (
	sarifUploadAttempts = 4
	sarifUploadDelay    = 5 * time.Second
)
