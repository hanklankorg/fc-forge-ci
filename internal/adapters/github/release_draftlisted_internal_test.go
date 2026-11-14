// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// draftListedLate answers a draft creation with release 7, and lists that
// release only from the listing numbered listedFrom, as GitHub lists a new
// release about a second after it created it. The first listing is the one
// CreateRelease makes before it creates anything.
func draftListedLate(listedFrom int, trace *[]string) *Provider {
	listings := 0

	return &Provider{
		APIBaseOverride:     "https://github.invalid",
		draftListRetryDelay: time.Nanosecond,
		HTTPClient: &http.Client{Transport: contractTransport(func(req *http.Request) (*http.Response, error) {
			*trace = append(*trace, req.Method+" "+req.URL.Path)

			if req.Method == http.MethodPost {
				return contractResponse(req, http.StatusCreated, `{"id":7,"tag_name":"v1","draft":true}`), nil
			}

			listings++
			if listings >= listedFrom {
				return contractResponse(req, http.StatusOK, `[{"id":7,"tag_name":"v1","draft":true}]`), nil
			}

			return contractResponse(req, http.StatusOK, `[]`), nil
		})},
	}
}

// A draft release without files was read back a second after its creation,
// before GitHub listed it, and the readback found no release. CreateRelease
// returns once the list shows the draft.
func TestCreateRelease_ReturnsOnceTheNewDraftIsListed(t *testing.T) {
	t.Parallel()

	var trace []string

	err := draftListedLate(4, &trace).CreateRelease(t.Context(), "o/r", provider.ReleaseSpec{Tag: "v1", Draft: true})
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET /repos/o/r/releases",
		"POST /repos/o/r/releases",
		"GET /repos/o/r/releases",
		"GET /repos/o/r/releases",
		"GET /repos/o/r/releases",
	}, trace)
}

// A draft the list never shows is an error that names the tag: the commands
// after this one could not find the release either.
func TestCreateRelease_FailsWhenTheNewDraftIsNeverListed(t *testing.T) {
	t.Parallel()

	var trace []string

	err := draftListedLate(1000, &trace).CreateRelease(t.Context(), "o/r", provider.ReleaseSpec{Tag: "v1", Draft: true})
	require.ErrorIs(t, err, errs.ErrDependencyUnavailable)
	require.ErrorContains(t, err, `draft release "v1" was created but the release list does not show it`)
	require.Len(t, trace, 2+draftListAttempts)
}
