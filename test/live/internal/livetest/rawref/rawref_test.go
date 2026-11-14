// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package rawref_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/test/live/internal/livetest/rawref"
)

// roundTripFunc serves the oracle's requests from a handler in memory, so the
// wire shape it reads is exercised without a socket.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func inMemoryClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)

		return recorder.Result(), nil
	})}
}

func TestReleaseByTag_AssetRedirectsRespectTheCallerAndKeepErrorsPrivate(t *testing.T) {
	t.Parallel()

	const token = "synthetic-private-token"

	for _, outcome := range []string{"same origin", "caller refusal", "transport failure", "HTTP failure"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()

			var redirects int

			client := inMemoryClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.EscapedPath() {
				case "/api/v4/projects/org%2Frepo/releases/v1":
					_, _ = writer.Write([]byte(`{"assets":{"links":[{"name":"a","url":"https://forge.invalid/start?credential=` + token + `"}]}}`))
				case "/start":
					if outcome == "HTTP failure" {
						http.Error(writer, token, http.StatusForbidden)

						return
					}

					http.Redirect(writer, request, "/asset", http.StatusFound)
				case "/asset":
					if request.Header.Get("PRIVATE-TOKEN") != token {
						t.Error("same-origin redirect lost authentication")
					}

					_, _ = writer.Write([]byte("asset"))
				default:
					http.NotFound(writer, request)
				}
			}))

			client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
				redirects++

				if outcome == "caller refusal" {
					return fmt.Errorf("reflected %s", token)
				}

				return nil
			}
			if outcome == "transport failure" {
				transport := client.Transport
				client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/start" {
						return nil, fmt.Errorf("reflected %s", token)
					}

					return transport.RoundTrip(r)
				})
			}

			reader := rawref.Reader{Forge: rawref.GitLab, Base: "https://forge.invalid", Owner: "org", Token: token, Client: client}

			release, found, err := rawref.ReleaseByTag(t.Context(), reader, "repo", "v1")
			if outcome == "same origin" {
				if err != nil || !found || len(release.Assets) != 1 || release.Assets[0].Size != 5 || redirects != 1 {
					t.Fatalf("release=%+v found=%v redirects=%d err=%v", release, found, redirects, err)
				}
			} else if !errors.Is(err, rawref.ErrForge) || strings.Contains(err.Error(), token) {
				t.Fatalf("error = %v, want a credential-free ErrForge", err)
			}
		})
	}
}

// Private browser uploads use session authentication, not PRIVATE-TOKEN. A
// successful release API read does not make the browser's 200 login page an
// asset. Exercise both URL shapes returned by the project uploads API.
func TestReleaseByTag_PrivateGitLabUploads(t *testing.T) {
	t.Parallel()

	uploadID := strings.Repeat("a", 32)

	const body = "the uploaded private release bytes\n"

	for _, browserPath := range []string{"/org/repo/uploads/", "/-/project/42/uploads/"} {
		t.Run(browserPath, func(t *testing.T) {
			t.Parallel()

			var browserReads, apiReads int

			handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.EscapedPath() {
				case "/api/v4/projects/org%2Frepo/releases/v1":
					_, _ = fmt.Fprintf(writer, `{"tag_name":"v1","assets":{"links":[{"name":"a.txt","url":"https://forge.invalid%s%sa.txt"}]}}`, browserPath, uploadID+"/")
				case "/api/v4/projects/org%2Frepo/uploads/" + uploadID + "/a.txt", "/api/v4/projects/42/uploads/" + uploadID + "/a.txt":
					apiReads++

					if request.Header.Get("PRIVATE-TOKEN") != "private-token" {
						http.Error(writer, "unauthorized", http.StatusUnauthorized)

						return
					}

					_, _ = writer.Write([]byte(body))
				case browserPath + uploadID + "/a.txt":
					browserReads++

					http.Redirect(writer, request, "/users/sign_in", http.StatusFound)
				case "/users/sign_in":
					writer.Header().Set("Content-Type", "text/html")
					_, _ = writer.Write([]byte("<html>Sign in to GitLab</html>"))
				default:
					http.NotFound(writer, request)
				}
			})
			reader := rawref.Reader{Forge: rawref.GitLab, Base: "https://forge.invalid", Owner: "org", Token: "private-token", Client: inMemoryClient(handler)}

			release, found, err := rawref.ReleaseByTag(t.Context(), reader, "repo", "v1")
			if err != nil || !found {
				t.Fatalf("release: found=%v err=%v", found, err)
			}

			sum := sha256.Sum256([]byte(body))

			want := rawref.Asset{Name: "a.txt", Size: int64(len(body)), Digest: hex.EncodeToString(sum[:])}
			if len(release.Assets) != 1 || release.Assets[0] != want {
				t.Errorf("assets = %+v, want %+v (not the login page)", release.Assets, want)
			}

			if apiReads != 1 || browserReads != 0 {
				t.Errorf("uploads API reads=%d browser reads=%d, want 1/0", apiReads, browserReads)
			}
		})
	}
}

func TestReleaseByTag_RejectsMalformedGitLabProjectUpload(t *testing.T) {
	t.Parallel()

	var assetReads int

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v4/projects/org/repo/releases/v1" {
			_, _ = fmt.Fprintf(writer, `{"assets":{"links":[{"name":"a","url":"https://forge.invalid/-/project/not-a-number/uploads/%s/a"}]}}`, strings.Repeat("a", 32))

			return
		}

		assetReads++
		_, _ = writer.Write([]byte("must not be fetched"))
	})
	reader := rawref.Reader{Forge: rawref.GitLab, Base: "https://forge.invalid", Owner: "org", Token: "private-token", Client: inMemoryClient(handler)}

	_, _, err := rawref.ReleaseByTag(t.Context(), reader, "repo", "v1")
	if !errors.Is(err, rawref.ErrForge) || assetReads != 0 {
		t.Fatalf("malformed upload: err=%v asset reads=%d; want ErrForge before any asset request", err, assetReads)
	}
}

func TestReleaseByTag_RefusesCredentialEscapeAndLogin(t *testing.T) {
	t.Parallel()

	for _, destination := range []string{
		"https://foreign.invalid/asset?secret=do-not-print",
		"https://sub.forge.invalid/asset",
		"http://forge.invalid/asset",
		"https://forge.invalid:444/asset",
		"https://user:do-not-print@forge.invalid/asset",
		"https://forge.invalid/users/sign_in",
	} {
		for _, redirect := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/redirect=%v", destination, redirect), func(t *testing.T) {
				t.Parallel()

				var unsafeReads int

				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.EscapedPath() {
					case "/api/v4/projects/org%2Frepo/releases/v1":
						assetURL := destination
						if redirect {
							assetURL = "https://forge.invalid/redirect"
						}

						_, _ = fmt.Fprintf(w, `{"assets":{"links":[{"name":"a","url":%q}]}}`, assetURL)
					case "/redirect":
						http.Redirect(w, r, destination, http.StatusFound)
					default:
						unsafeReads++
						_, _ = w.Write([]byte("should never be read"))
					}
				})
				reader := rawref.Reader{Forge: rawref.GitLab, Base: "https://forge.invalid", Owner: "org", Token: "private-token", Client: inMemoryClient(handler)}

				_, _, err := rawref.ReleaseByTag(t.Context(), reader, "repo", "v1")
				if !errors.Is(err, rawref.ErrForge) {
					t.Errorf("error = %v, want ErrForge", err)
				}

				if unsafeReads != 0 {
					t.Errorf("sent %d requests outside the credential boundary or to login", unsafeReads)
				}

				if err != nil && (strings.Contains(err.Error(), "do-not-print") || strings.Contains(err.Error(), "private-token")) {
					t.Errorf("error leaked credentials: %v", err)
				}
			})
		}
	}
}

// TestReleaseByTag_ReadsTheRecordedStateFromEachWire pins what the oracle
// reads off each forge: the identity fields, the policy flags Forgejo records
// (GitLab has none, so both stay false there), and the digest of the bytes
// each asset actually serves rather than the size the forge reports.
func TestReleaseByTag_ReadsTheRecordedStateFromEachWire(t *testing.T) {
	t.Parallel()

	const body = "asset bytes\n"

	sum := sha256.Sum256([]byte(body))
	wantDigest := hex.EncodeToString(sum[:])

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/api/v1/repos/org/repo/releases/tags/v1.0.0-rc.1":
			if request.Header.Get("Authorization") != "token forgejo-token" {
				http.Error(response, "unauthorized", http.StatusUnauthorized)

				return
			}

			_, _ = response.Write([]byte(`{"tag_name":"v1.0.0-rc.1","name":"RC","body":"notes","draft":true,"prerelease":true,` +
				`"assets":[{"name":"a.txt","size":12,"browser_download_url":"https://forge.invalid/dl/a.txt"}]}`))
		case request.URL.Path == "/api/v4/projects/org%2Frepo/releases/v1.0.0" || request.URL.RawPath == "/api/v4/projects/org%2Frepo/releases/v1.0.0":
			if request.Header.Get("PRIVATE-TOKEN") != "gitlab-token" {
				http.Error(response, "unauthorized", http.StatusUnauthorized)

				return
			}

			_, _ = response.Write([]byte(`{"tag_name":"v1.0.0","name":"One","description":"notes","assets":{"links":[{"name":"a.txt","url":"https://forge.invalid/dl/a.txt"}]}}`))
		case request.URL.Path == "/dl/a.txt":
			_, _ = response.Write([]byte(body))
		default:
			http.NotFound(response, request)
		}
	})

	forgejo := rawref.Reader{Forge: rawref.Forgejo, Base: "https://forge.invalid", Token: "forgejo-token", Owner: "org", Client: inMemoryClient(handler)}

	release, found, err := rawref.ReleaseByTag(context.Background(), forgejo, "repo", "v1.0.0-rc.1")
	if err != nil || !found {
		t.Fatalf("forgejo: found=%v err=%v", found, err)
	}

	if release.Tag != "v1.0.0-rc.1" || release.Name != "RC" || release.Body != "notes" || !release.Draft || !release.Prerelease {
		t.Errorf("forgejo release = %+v, want the recorded prerelease draft", release)
	}

	if len(release.Assets) != 1 || release.Assets[0] != (rawref.Asset{Name: "a.txt", Size: 12, Digest: wantDigest}) {
		t.Errorf("forgejo assets = %+v, want a.txt with the served digest", release.Assets)
	}

	gitlab := rawref.Reader{Forge: rawref.GitLab, Base: "https://forge.invalid", Token: "gitlab-token", Owner: "org", Client: inMemoryClient(handler)}

	release, found, err = rawref.ReleaseByTag(context.Background(), gitlab, "repo", "v1.0.0")
	if err != nil || !found {
		t.Fatalf("gitlab: found=%v err=%v", found, err)
	}

	if release.Tag != "v1.0.0" || release.Name != "One" || release.Body != "notes" || release.Draft || release.Prerelease {
		t.Errorf("gitlab release = %+v, want a published, non-prerelease release", release)
	}

	if len(release.Assets) != 1 || release.Assets[0] != (rawref.Asset{Name: "a.txt", Size: int64(len(body)), Digest: wantDigest}) {
		t.Errorf("gitlab assets = %+v, want a.txt with the served size and digest", release.Assets)
	}

	// Absence is reported as not found, never as an empty release.
	if _, found, err := rawref.ReleaseByTag(context.Background(), forgejo, "repo", "v9.9.9"); err != nil || found {
		t.Errorf("missing release: found=%v err=%v", found, err)
	}

	if _, _, err := rawref.ReleaseByTag(context.Background(), rawref.Reader{Forge: "github"}, "repo", "v1"); err == nil || !strings.Contains(err.Error(), "no reader") {
		t.Errorf("unknown forge err = %v", err)
	}
}
