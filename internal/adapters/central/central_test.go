// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package central_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/central"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCentralStreamsBundleAndObservesExactDeployment(t *testing.T) {
	t.Parallel()

	var uploads, observations atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "Bearer "+base64.StdEncoding.EncodeToString([]byte("fixture:synthetic-token")), r.Header.Get("Authorization"))

		switch r.URL.Path {
		case "/upload":
			uploads.Add(1)
			assert.Equal(t, "AUTOMATIC", r.URL.Query().Get("publishingType"))

			reader, err := r.MultipartReader()
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusBadRequest)

				return
			}

			part, err := reader.NextPart()
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusBadRequest)

				return
			}

			body, err := io.ReadAll(part)
			assert.NoError(t, err)
			assert.Equal(t, "bundle", part.FormName())
			assert.Equal(t, "fixture bundle", string(body))

			_, _ = io.WriteString(w, "deployment-1")
		case "/status":
			observations.Add(1)
			assert.Equal(t, "deployment-1", r.URL.Query().Get("id"))

			_, _ = io.WriteString(w, `{"deploymentState":"PUBLISHED"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	file := filepath.Join(t.TempDir(), "bundle.zip")
	require.NoError(t, os.WriteFile(file, []byte("fixture bundle"), 0o600))

	client := central.Client{BaseURL: server.URL, Username: "fixture", Password: "synthetic-token"}
	id, err := client.Upload(t.Context(), file, true)
	require.NoError(t, err)
	require.Equal(t, "deployment-1", id)
	state, err := client.State(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "PUBLISHED", state)
	require.EqualValues(t, 1, uploads.Load())
	require.EqualValues(t, 1, observations.Load())
}

func TestCentralRefusesRedirectsAndNeverPrintsResponseCredentials(t *testing.T) {
	t.Parallel()

	var leaked atomic.Int64

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); w.WriteHeader(http.StatusOK) }))
	defer other.Close()

	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusForbidden} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", other.URL)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "synthetic-token and private response text")
		}))
		client := central.Client{BaseURL: server.URL, Username: "fixture", Password: "synthetic-token"}
		_, err := client.State(t.Context(), "deployment-1")
		require.Error(t, err)
		require.NotContains(t, err.Error(), "synthetic-token")
		require.NotContains(t, err.Error(), "private response")
		server.Close()
	}

	require.Zero(t, leaked.Load())

	client := central.Client{BaseURL: other.URL, Username: "fixture", Password: "synthetic-token"}
	_, err := client.State(t.Context(), "../wrong")
	require.ErrorIs(t, err, errs.ErrUsage)
	require.Zero(t, leaked.Load())
}

func TestCentralBoundsStatusResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", (1<<20)+1)) }))
	defer server.Close()

	_, err := (central.Client{BaseURL: server.URL, Username: "fixture", Password: "synthetic-token"}).State(t.Context(), "deployment-1")
	require.ErrorIs(t, err, errs.ErrMalformedInput)
}

func TestCentralRejectsUnsafeOriginsBeforeSendingCredentials(t *testing.T) {
	t.Parallel()

	for _, origin := range []string{
		"http://central.example/api", "ftp://central.example/api", "/api",
		"https://user:password@central.example/api", "https://central.example/api?redirect=other",
		"https://central.example/api#fragment",
	} {
		t.Run(origin, func(t *testing.T) {
			client := central.Client{BaseURL: origin, Username: "fixture", Password: "synthetic-token"}
			_, err := client.State(t.Context(), "deployment-1")
			require.ErrorIs(t, err, errs.ErrInvalidConfig)
			require.NotContains(t, err.Error(), "synthetic-token")
		})
	}
}
