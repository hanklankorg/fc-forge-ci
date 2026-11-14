// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-github/v91/github"
)

func TestPrivateImageProbeRequiresActualAccessDenial(t *testing.T) {
	t.Parallel()

	for _, status := range []int{200, 401, 403, 404, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			requests := 0

			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Authorization") != "" {
					t.Error("anonymous probe forwarded credentials")
				}

				if request.URL.Path == "/v2/" {
					response.WriteHeader(http.StatusOK)

					return
				}

				if request.Method != http.MethodGet || request.URL.Path != "/v2/owner/private/manifests/v1" {
					t.Errorf("probe changed the exact reference: %s %s", request.Method, request.URL.Path)
				}

				requests++

				response.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				response.WriteHeader(status)
				_, _ = response.Write([]byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":2},"layers":[]}`))
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
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			var authenticated, anonymous []string

			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.Header.Get("Accept") != "application/octet-stream" {
					t.Errorf("expected a content GET: %s %s", request.Method, request.Header.Get("Accept"))
				}

				if request.Header.Get("Authorization") == "Bearer inert-token" {
					authenticated = append(authenticated, request.URL.Path)

					response.Header().Set("Content-Type", "application/octet-stream")
					_, _ = response.Write([]byte("authenticated fixture bytes"))

					return
				}

				if request.Header.Get("Authorization") != "" {
					t.Error("anonymous asset probe forwarded credentials")
				}

				anonymous = append(anonymous, request.URL.Path)

				if status == 302 {
					response.Header().Set("Location", "/must-not-follow")
				}

				response.WriteHeader(status)
			}))
			defer server.Close()

			client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil), github.WithAuthToken("inert-token"))
			if err != nil {
				t.Fatal(err)
			}

			tb := &recordingTB{}
			trial := &GitHubTrial{tb: tb, api: client, org: "example-org"}
			trial.CheckPrivateReleaseAssets("fc-private", map[string]int64{"worker-linux-amd64": 42})

			denied := status == 401 || status == 403 || status == 404
			if len(authenticated) != 1 || len(anonymous) != 1 || authenticated[0] != anonymous[0] || authenticated[0] != "/repos/example-org/fc-private/releases/assets/42" || (len(tb.errors) == 0) != denied {
				t.Fatalf("auth=%v anonymous=%v status=%d verdict=%+v", authenticated, anonymous, status, tb)
			}
		})
	}
}
