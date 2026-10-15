// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitHubUploadURL_UsesEnterpriseUploadsEndpoint(t *testing.T) {
	t.Parallel()

	api, err := url.Parse("https://github.example.test/api/v3/")
	if err != nil {
		t.Fatal(err)
	}

	if got := githubUploadURL(api).String(); got != "https://github.example.test/api/uploads/" {
		t.Fatalf("upload URL = %q", got)
	}

	if got := api.String(); got != "https://github.example.test/api/v3/" {
		t.Fatalf("API URL was mutated: %q", got)
	}
}

func TestGitHubUploadURL_TestOverrideKeepsSharedBase(t *testing.T) {
	t.Parallel()

	api, err := url.Parse("https://fixture.example.test/")
	if err != nil {
		t.Fatal(err)
	}

	if got := githubUploadURL(api).String(); got != api.String() {
		t.Fatalf("upload URL = %q, want %q", got, api)
	}
}

// Cloud API roots with and without a trailing slash must both retain GitHub's
// separate upload host. Exercise the public operation so the configured SDK
// client, source precedence and upload request all participate.
func TestUploadReleaseAsset_UsesConfiguredEndpoints(t *testing.T) {
	t.Parallel()

	const (
		cloudAPI    = "https://api.github.com/"
		cloudUpload = "https://uploads.github.com/"
		releasePath = "repos/owner/repo/releases/"
	)

	for _, test := range []struct {
		name, envBase, override, api, upload string
	}{
		{name: "default", api: cloudAPI, upload: cloudUpload},
		{name: "cloud environment", envBase: "https://api.github.com", api: cloudAPI, upload: cloudUpload},
		{name: "cloud environment with slash", envBase: cloudAPI, api: cloudAPI, upload: cloudUpload},
		{name: "cloud override", override: "https://api.github.com", api: cloudAPI, upload: cloudUpload},
		{
			name: "cloud override with slash wins over environment", envBase: "https://ignored.invalid/api/v3/",
			override: cloudAPI, api: cloudAPI, upload: cloudUpload,
		},
		{
			name: "enterprise environment", envBase: "https://enterprise.invalid/api/v3/",
			api: "https://enterprise.invalid/api/v3/", upload: "https://enterprise.invalid/api/uploads/",
		},
		{
			name: "fixture override wins over environment", envBase: cloudAPI, override: "https://fixture.invalid/",
			api: "https://fixture.invalid/", upload: "https://fixture.invalid/",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			file := filepath.Join(t.TempDir(), "app.bin")
			require.NoError(t, os.WriteFile(file, []byte("bin"), 0o600))

			env := map[string]string{
				"GITHUB_API_URL": test.envBase, "GITHUB_REPOSITORY": "owner/repo", "GH_TOKEN": "synthetic-upload-token",
			}

			var requests []string

			uploaded := false
			p := &Provider{
				Env:             func(key string) string { return env[key] },
				APIBaseOverride: test.override,
				HTTPClient: &http.Client{Transport: contractTransport(func(req *http.Request) (*http.Response, error) {
					target := req.Method + " " + req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
					requests = append(requests, target)

					require.Equal(t, "Bearer synthetic-upload-token", req.Header.Get("Authorization"))

					switch target {
					case "GET " + test.api + releasePath + "tags/v1":
						return contractResponse(req, http.StatusOK, `{"id":42}`), nil
					case "GET " + test.api + releasePath + "42/assets":
						return contractResponse(req, http.StatusOK, `[]`), nil
					case "POST " + test.upload + releasePath + "42/assets":
						body, err := io.ReadAll(req.Body)
						require.NoError(t, err)
						require.Equal(t, "bin", string(body))
						require.Equal(t, "app.bin", req.URL.Query().Get("name"))

						uploaded = true

						return contractResponse(req, http.StatusCreated, fmt.Sprintf(
							`{"id":7,"name":"app.bin","size":3,"digest":"sha256:%x"}`, sha256.Sum256(body))), nil
					default:
						return contractResponse(req, http.StatusNotFound, `{"message":"wrong endpoint"}`), nil
					}
				})},
			}

			require.NoError(t, p.UploadReleaseAsset(t.Context(), "v1", file), "requests: %v", requests)
			require.True(t, uploaded, "no asset reached the upload endpoint")
		})
	}
}
