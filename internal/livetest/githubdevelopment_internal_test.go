// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	githubapi "github.com/google/go-github/v91/github"
)

func developmentArchive(t *testing.T) []byte {
	t.Helper()

	var data bytes.Buffer

	archive := zip.NewWriter(&data)

	file, err := archive.CreateHeader(&zip.FileHeader{Name: "build/output.txt", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := file.Write([]byte("development artifact")); err != nil {
		t.Fatal(err)
	}

	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	return data.Bytes()
}

func TestDevelopmentAcceptanceChecksArtifactsAndStableState(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, want                                 string
		changed, tag, noArtifact, damaged, expired bool
		release, wrongDigest, empty, badDownload   bool
		badContents, paginated                     bool
	}{
		{name: "downloaded artifact", want: ""},
		{name: "main mutated", changed: true, want: "changed main"},
		{name: "stable tag created", tag: true, want: "created a tag"},
		{name: "no artifact", noArtifact: true, want: "no downloadable artifacts"},
		{name: "damaged archive", damaged: true, want: "not a valid ZIP"},
		{name: "expired artifact", expired: true, want: "expired"},
		{name: "stable release created", release: true, want: "created a release"},
		{name: "digest mismatch", wrongDigest: true, want: "digest differs"},
		{name: "empty archive", empty: true, want: "contains no files"},
		{name: "download refused", badDownload: true, want: "download returned HTTP 403"},
		{name: "damaged member CRC", badContents: true, want: "damaged or over-budget contents"},
		{name: "all artifact pages downloaded", paginated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data := developmentArchive(t)
			if tc.damaged {
				data = []byte("not an archive")
			}

			if tc.empty {
				var buffer bytes.Buffer
				if err := zip.NewWriter(&buffer).Close(); err != nil {
					t.Fatal(err)
				}

				data = buffer.Bytes()
			}

			if tc.badContents {
				// Stored payload begins after the local header and filename.
				// Metadata still hashes the downloaded ZIP, so only reading the
				// complete member and validating its CRC detects this corruption.
				data[30+len("build/output.txt")] ^= 0xff
			}

			digest := sha256.Sum256(data)
			if tc.wrongDigest {
				digest[0] ^= 0xff
			}

			var downloads atomic.Int32

			blob := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("API credential forwarded to signed artifact URL")
				}

				if r.URL.Query().Get("signature") != "private-query" {
					t.Error("artifact URL lost its signing query")
				}

				downloads.Add(1)

				if tc.badDownload {
					response.WriteHeader(http.StatusForbidden)

					return
				}

				_, _ = response.Write(data)
			}))
			defer blob.Close()

			api := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")

				switch {
				case strings.Contains(request.URL.Path, "/git/ref/"):
					sha := "original"
					if tc.changed {
						sha = "changed"
					}

					_ = json.NewEncoder(response).Encode(map[string]any{"object": map[string]string{"sha": sha}})
				case strings.HasSuffix(request.URL.Path, "/tags"):
					if tc.tag {
						_, _ = response.Write([]byte(`[{"name":"v1.0.0"}]`))
					} else {
						_, _ = response.Write([]byte(`[]`))
					}
				case strings.HasSuffix(request.URL.Path, "/releases"):
					if tc.release {
						_, _ = response.Write([]byte(`[{"id":1}]`))
					} else {
						_, _ = response.Write([]byte(`[]`))
					}
				case strings.Contains(request.URL.Path, "/actions/runs/") && strings.HasSuffix(request.URL.Path, "/artifacts"):
					if tc.noArtifact {
						_, _ = response.Write([]byte(`{"total_count":0,"artifacts":[]}`))

						return
					}

					id, total := 1, 1
					if tc.paginated {
						total = 2

						if request.URL.Query().Get("page") == "2" {
							id = 2
						} else {
							response.Header().Set("Link", "<http://"+request.Host+request.URL.Path+"?page=2>; rel=\"next\"")
						}
					}

					_ = json.NewEncoder(response).Encode(map[string]any{"total_count": total, "artifacts": []map[string]any{{
						"id": id, "name": "dev-files", "expired": tc.expired, "digest": "sha256:" + hex.EncodeToString(digest[:]),
					}}})
				case strings.HasSuffix(request.URL.Path, "/artifacts/1/zip"), strings.HasSuffix(request.URL.Path, "/artifacts/2/zip"):
					http.Redirect(response, request, blob.URL+"?signature=private-query", http.StatusFound)
				default:
					t.Errorf("unexpected API request: %s", request.URL.Path)
					response.WriteHeader(http.StatusNotFound)
				}
			}))
			defer api.Close()

			client, err := githubapi.NewClient(githubapi.WithHTTPClient(api.Client()), githubapi.WithAuthToken("inert-api-token"), githubapi.WithURLs(new(api.URL+"/"), nil))
			if err != nil {
				t.Fatal(err)
			}

			trial := &GitHubTrial{tb: stopOnFatalTB{TB: t}, api: client, org: "example-org"}
			failure := ""

			func() {
				defer func() {
					if caught := recover(); caught != nil {
						message, ok := caught.(acceptanceFailure)
						if !ok {
							panic(caught)
						}

						failure = string(message)
					}
				}()

				trial.CheckDevelopmentRelease("fc-development", "original", 7)
			}()

			if tc.want == "" {
				wantDownloads := int32(1)
				if tc.paginated {
					wantDownloads = 2
				}

				if failure != "" || downloads.Load() != wantDownloads {
					t.Fatalf("failure=%s downloads=%d", failure, downloads.Load())
				}
			} else if !strings.Contains(failure, tc.want) || strings.Contains(failure, "private-query") {
				t.Fatalf("failure=%q, want %q without private download query", failure, tc.want)
			}
		})
	}
}

func TestDevelopmentDispatchIsSentOnceAndReturnsIdentity(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		var body githubapi.CreateWorkflowDispatchEventRequest
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.Ref != "main" || body.ReturnRunDetails == nil || !*body.ReturnRunDetails {
			t.Error("invalid development dispatch")
		}

		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"workflow_run_id":7,"html_url":"https://github.com/example-org/fc-development/actions/runs/7"}`))
	}))
	defer server.Close()

	client, err := githubapi.NewClient(githubapi.WithHTTPClient(server.Client()), githubapi.WithURLs(new(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}

	trial := &GitHubTrial{tb: t, api: client, org: "example-org"}
	if id := trial.DispatchDevelopment("fc-development", "release-dev.yml"); id != 7 || calls.Load() != 1 {
		t.Fatalf("dispatch id=%d calls=%d", id, calls.Load())
	}
}
