// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
)

// CheckPrivateReleaseAssets pairs an authenticated content GET with an anonymous
// GET of each identical API asset coordinate. Positive content/signature checks
// belong to CheckRelease; this checks the access boundary before repo cleanup.
func (trial *GitHubTrial) CheckPrivateReleaseAssets(repo string, assets map[string]int64) {
	trial.tb.Helper()
	base, err := url.Parse(trial.api.BaseURL())
	if err != nil {
		trial.tb.Errorf("cannot construct anonymous release-asset API endpoint")
		return
	}
	for name, id := range assets {
		ctx, cancel := context.WithTimeout(trial.tb.Context(), time.Minute)
		body, _, err := trial.api.Repositories.DownloadReleaseAsset(ctx, trial.org, repo, id, &http.Client{Timeout: 30 * time.Second})
		if err != nil || body == nil {
			cancel()
			trial.tb.Errorf("private release asset %s lacks an authenticated content response", name)
			continue
		}
		_ = body.Close()
		coordinate := fmt.Sprintf("repos/%s/%s/releases/assets/%d", url.PathEscape(trial.org), url.PathEscape(repo), id)
		endpoint := base.ResolveReference(&url.URL{Path: coordinate})
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			cancel()
			trial.tb.Errorf("cannot construct anonymous release-asset probe")
			continue
		}
		request.Header.Set("Accept", "application/octet-stream")
		client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			cancel()
			trial.tb.Errorf("anonymous release-asset probe did not establish access denial")
			continue
		}
		_ = response.Body.Close()
		cancel()
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			trial.tb.Logf("private release asset %s: authenticated content GET succeeded; same anonymous path denied (HTTP %d)", name, response.StatusCode)
		default:
			trial.tb.Errorf("private release asset %s anonymous denial failed: HTTP %d", name, response.StatusCode)
		}
	}
}

// CheckPrivateImage requires anonymous denial on the exact reference that just
// passed authenticated image verification. An outage or TLS failure is not
// evidence of privacy. The existing anonymous registry adapter never consults
// the host's Docker credentials, including those used for the positive check.
func (trial *GitHubTrial) CheckPrivateImage(verifiedReference string) {
	trial.tb.Helper()
	ctx, cancel := context.WithTimeout(trial.tb.Context(), time.Minute)
	defer cancel()
	_, err := ociregistry.NewAnonymous().Manifest(ctx, verifiedReference)
	var response *transport.Error
	if errors.As(err, &response) {
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			trial.tb.Logf("private image %s: authenticated verification passed; same anonymous reference denied (HTTP %d)", verifiedReference, response.StatusCode)
			return
		}
	}
	// Avoid printing registry response bodies or a credential-bearing transport
	// diagnostic; failure classification is enough to judge this privacy claim.
	if err == nil {
		trial.tb.Errorf("private image %s is anonymously readable", verifiedReference)
	} else {
		trial.tb.Errorf("anonymous image probe for %s did not establish HTTP 401/403/404 denial", verifiedReference)
	}
}
