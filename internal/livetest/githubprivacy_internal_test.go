// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v91/github"
)

func TestPrivateImageProbeRequiresActualAccessDenial(t *testing.T) {
	t.Parallel()
	for _, status := range []int{200, 401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("anonymous probe forwarded credentials")
				}
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/v2/owner/private/manifests/v1" {
					t.Errorf("probe changed the exact reference: %s %s", r.Method, r.URL.Path)
				}
				requests++
				w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":2},"layers":[]}`))
			}))
			defer server.Close()
			tb := &recordingTB{}
			trial := &GitHubTrial{tb: tb}
			trial.CheckPrivateImage(strings.TrimPrefix(server.URL, "http://") + "/owner/private:v1")
			denied := status == 401 || status == 403 || status == 404
			if requests == 0 || (len(tb.errors) == 0) != denied || tb.fatal != "" {
				t.Fatalf("status=%d requests=%d verdict=%+v", status, requests, tb)
			}
		})
	}
}

func TestPrivateReleaseAssetsPairExactAuthenticatedAndAnonymousPaths(t *testing.T) {
	t.Parallel()
	for _, status := range []int{200, 302, 401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			var authenticated, anonymous []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/octet-stream" {
					t.Errorf("expected a content GET: %s %s", r.Method, r.Header.Get("Accept"))
				}
				if r.Header.Get("Authorization") == "Bearer inert-token" {
					authenticated = append(authenticated, r.URL.Path)
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write([]byte("authenticated fixture bytes"))
					return
				}
				if r.Header.Get("Authorization") != "" {
					t.Error("anonymous asset probe forwarded credentials")
				}
				anonymous = append(anonymous, r.URL.Path)
				if status == 302 {
					w.Header().Set("Location", "/must-not-follow")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil), github.WithAuthToken("inert-token"))
			if err != nil {
				t.Fatal(err)
			}
			tb := &recordingTB{}
			trial := &GitHubTrial{tb: tb, api: client, org: "hanklankorg"}
			trial.CheckPrivateReleaseAssets("fc-private", map[string]int64{"worker-linux-amd64": 42})
			denied := status == 401 || status == 403 || status == 404
			if len(authenticated) != 1 || len(anonymous) != 1 || authenticated[0] != anonymous[0] || authenticated[0] != "/repos/hanklankorg/fc-private/releases/assets/42" || (len(tb.errors) == 0) != denied {
				t.Fatalf("auth=%v anonymous=%v status=%d verdict=%+v", authenticated, anonymous, status, tb)
			}
		})
	}
}
