// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestArtifactReadRetriesInternalServerError(t *testing.T) {
	t.Parallel()

	for _, permanent := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "exhaustion"}[permanent], func(t *testing.T) {
			t.Parallel()

			reads := 0
			srv := serveRuntime(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/_apis/pipelines/workflows/7/artifacts":
					writeJSON(response, jsonObj{"value": []jsonObj{{"name": "dist", "fileContainerResourceUrl": runtimeFakeURL + "/container"}}})
				case "/container":
					writeJSON(response, jsonObj{"value": []jsonObj{{"path": "dist/sbom.json", "itemType": "file", "contentLocation": runtimeFakeURL + "/file"}}})
				case "/file":
					reads++
					if reads < 3 || permanent {
						response.WriteHeader(http.StatusInternalServerError)
					} else {
						_, _ = response.Write([]byte("verified evidence"))
					}
				}
			}))
			srv.client.Transport = httpretry.NewTransport(httpretry.Config{
				Inner: srv.client.Transport, MaxAttempts: 3,
				BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond,
			})
			dir := t.TempDir()

			_, err := runtimeProvider(srv).DownloadRunArtifact(t.Context(), provider.RunArtifactDownload{Name: "dist", Dir: dir})
			if permanent {
				require.ErrorIs(t, err, errs.ErrDependencyUnavailable)
				require.ErrorContains(t, err, `artifact "dist" entry "sbom.json"`)
				require.ErrorContains(t, err, "HTTP 500, retries exhausted")
				require.NoFileExists(t, filepath.Join(dir, "sbom.json"))
			} else {
				require.NoError(t, err)

				body, readErr := os.ReadFile(filepath.Join(dir, "sbom.json"))
				require.NoError(t, readErr)
				require.Equal(t, "verified evidence", string(body))
			}

			require.Equal(t, 3, reads)
		})
	}
}
