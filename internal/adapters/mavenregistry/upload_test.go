// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package mavenregistry_test

import (
	"github.com/diggsweden/forge-ci/internal/adapters/mavenregistry"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMavenUploadPreservesLiteralFilenameAndRefusesRedirect(t *testing.T) {
	t.Parallel()

	var redirected atomic.Int64

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer other.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "/packages/demo-%2f%2e%2e.jar", req.URL.Path)
		w.Header().Set("Location", other.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	registry := provider.ForgeMavenRegistry{URL: server.URL + "/packages", AuthScheme: provider.MavenAuthTokenHeader, Token: "synthetic"}
	err := (mavenregistry.Client{}).Upload(t.Context(), registry, "demo-%2f%2e%2e.jar", strings.NewReader("fixture"), 7)
	require.Error(t, err)
	require.Zero(t, redirected.Load())
}

func TestMavenUploadUsesResolvedAuthenticationAndExactBytes(t *testing.T) {
	t.Parallel()

	for _, scheme := range []provider.MavenAuthScheme{provider.MavenAuthServerPassword, provider.MavenAuthJobTokenHeader, provider.MavenAuthTokenHeader} {
		t.Run(string(scheme), func(t *testing.T) {
			t.Parallel()

			requests := 0

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++

				assert.Equal(t, "/packages/org/example/demo/1.2.3/demo-1.2.3.jar", r.URL.Path)
				assert.Equal(t, http.MethodPut, r.Method)
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.Equal(t, "fixture", string(body))

				switch scheme {
				case provider.MavenAuthServerPassword:
					user, token, ok := r.BasicAuth()
					assert.True(t, ok)
					assert.Equal(t, "user", user)
					assert.Equal(t, "synthetic", token)
				case provider.MavenAuthJobTokenHeader:
					assert.Equal(t, "synthetic", r.Header.Get("Job-Token"))
				case provider.MavenAuthTokenHeader:
					assert.Equal(t, "token synthetic", r.Header.Get("Authorization"))
				}

				w.WriteHeader(http.StatusCreated)
			}))
			defer server.Close()

			err := (mavenregistry.Client{}).Upload(t.Context(), provider.ForgeMavenRegistry{URL: server.URL + "/packages", AuthScheme: scheme, Username: "user", Token: "synthetic"}, "org/example/demo/1.2.3/demo-1.2.3.jar", strings.NewReader("fixture"), 7)
			require.NoError(t, err)
			require.Equal(t, 1, requests)
		})
	}
}
