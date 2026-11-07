// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBranchHeadUsesAuthenticatedSDKAndAcceptsFullObjectIDs(t *testing.T) {
	t.Parallel()

	for _, size := range []int{40, 64} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()

			sha := strings.Repeat("a", size)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/repos/acme/widget/branches/main" {
					t.Errorf("unexpected branch endpoint: %s", r.URL.Path)
				}

				if r.Header.Get("Authorization") != "token fixture-token" {
					t.Error("branch request must carry the source forge credential")
				}

				_, _ = fmt.Fprintf(w, `{"name":"main","commit":{"id":%q}}`, sha)
			}))
			defer server.Close()

			adapter := &Provider{APIBaseOverride: server.URL, HTTPClient: server.Client(), Env: func(name string) string {
				if name == "FORGEJO_TOKEN" {
					return "fixture-token"
				}

				return ""
			}}
			got, err := adapter.BranchHead(t.Context(), server.URL, "acme/widget", "main")
			require.NoError(t, err)
			require.Equal(t, sha, got)
		})
	}
}

func TestBranchHeadRefusesForeignSourceAndMalformedReplies(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`null`, `{}`, `{"commit":null}`, `{"commit":{"id":"short"}}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int64

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)

				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()

			adapter := &Provider{APIBaseOverride: server.URL, HTTPClient: server.Client(), Env: func(string) string { return "" }}
			_, err := adapter.BranchHead(t.Context(), "https://another.example", "acme/widget", "main")
			require.Error(t, err)
			require.Zero(t, calls.Load())
			_, err = adapter.BranchHead(t.Context(), server.URL, "acme/widget", "main")
			require.Error(t, err)
		})
	}
}

func TestBranchHeadCannotRedirectItsCredentialToAnotherAuthority(t *testing.T) {
	t.Parallel()

	var leaked atomic.Int64

	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer foreign.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/stolen", http.StatusFound)
	}))
	defer server.Close()

	adapter := &Provider{APIBaseOverride: server.URL, HTTPClient: server.Client(), Env: func(name string) string {
		if name == "FORGEJO_TOKEN" {
			return "fixture-token"
		}

		return ""
	}}
	_, err := adapter.BranchHead(t.Context(), server.URL, "acme/widget", "main")
	require.Error(t, err)
	require.Zero(t, leaked.Load())
}
