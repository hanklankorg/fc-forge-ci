// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/stretchr/testify/require"
)

func TestArtifactTransferDiagnostics_OmitSignedURLs(t *testing.T) {
	t.Parallel()

	const signedURL = "https://blob.invalid/private-artifact.zip?sig=SYNTHETIC-SAS-CREDENTIAL&se=2030"

	for _, mode := range []string{"download", "pattern", "upload"} {
		for name, cause := range map[string]error{
			"success":   nil,
			"dial":      &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			"cancelled": context.Canceled,
			"deadline":  context.DeadlineExceeded,
		} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				t.Parallel()

				var archive bytes.Buffer

				writer := zip.NewWriter(&archive)
				file, err := writer.Create("value.txt")
				require.NoError(t, err)
				_, err = io.WriteString(file, "fixture artifact")
				require.NoError(t, err)
				require.NoError(t, writer.Close())

				root := t.TempDir()
				upload := filepath.Join(root, "source.zip")
				require.NoError(t, os.WriteFile(upload, archive.Bytes(), 0o600))

				transport := &artifactDiagnosticTransport{t: t, signedURL: signedURL, archive: archive.Bytes(), cause: cause}
				p := &Provider{
					APIBaseOverride: "https://github.invalid",
					Env:             func(string) string { return "" },
					HTTPClient:      &http.Client{Transport: httpretry.NewTransport(httpretry.Config{Inner: transport, MaxAttempts: 1})},
				}
				destination := filepath.Join(root, "download")

				if mode == "upload" {
					_, err = p.runV4Upload(t.Context(), uploadContext{resultsURL: "https://results.invalid", token: "runtime-fixture", runID: "1", jobID: "2"}, "wanted", 1, zipArchive{path: upload, zipSize: int64(archive.Len())})
				} else {
					input := provider.RunArtifactDownload{Repository: "o/r", RunID: "1", Name: "wanted", Dir: destination, MergeMultiple: true}
					if mode == "pattern" {
						input.Pattern = "*"
					}

					_, err = p.DownloadRunArtifact(t.Context(), input)
				}

				require.Equal(t, 1, transport.blobCalls)

				if cause == nil {
					require.NoError(t, err)

					if mode == "upload" {
						require.Equal(t, 1, transport.finalizations)
					} else {
						body, readErr := os.ReadFile(filepath.Join(destination, "value.txt"))
						require.NoError(t, readErr)
						require.Equal(t, "fixture artifact", string(body))
					}

					return
				}

				require.ErrorIs(t, err, cause)
				require.Equal(t, errs.ExitCodeFromError(cause), errs.ExitCodeFromError(err))
				require.Zero(t, transport.finalizations, "a failed blob upload must not finalize")

				_, statErr := os.Stat(destination)
				require.ErrorIs(t, statErr, os.ErrNotExist, "a failed download must not publish files")

				var diagnostic bytes.Buffer

				slog.New(slog.NewTextHandler(&diagnostic, nil)).Error("artifact failed", "err", err)

				for _, text := range []string{err.Error(), fmt.Sprintf("%+v", err), diagnostic.String()} {
					require.Contains(t, text, "artifact")
					require.NotContains(t, text, "SYNTHETIC-SAS-CREDENTIAL")
					require.NotContains(t, text, "private-artifact.zip")
				}
			})
		}
	}
}

type artifactDiagnosticTransport struct {
	t             *testing.T
	signedURL     string
	archive       []byte
	cause         error
	blobCalls     int
	finalizations int
}

func (tr *artifactDiagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer func() { _ = req.Body.Close() }()
	}

	switch {
	case req.URL.Host == "blob.invalid":
		tr.blobCalls++

		require.Equal(tr.t, tr.signedURL, req.URL.String(), "the credential must still reach the blob store")
		require.Empty(tr.t, req.Header.Get("Authorization"))

		if tr.cause != nil {
			return nil, tr.cause
		}

		if req.Method == http.MethodPut {
			body, err := io.ReadAll(req.Body)
			require.NoError(tr.t, err)
			require.Equal(tr.t, tr.archive, body)

			return contractResponse(req, http.StatusCreated, ""), nil
		}

		return contractResponse(req, http.StatusOK, string(tr.archive)), nil
	case strings.HasSuffix(req.URL.Path, "/CreateArtifact"):
		return contractResponse(req, http.StatusOK, fmt.Sprintf(`{"ok":true,"signedUploadUrl":%q}`, tr.signedURL)), nil
	case strings.HasSuffix(req.URL.Path, "/FinalizeArtifact"):
		tr.finalizations++

		return contractResponse(req, http.StatusOK, `{"ok":true,"artifactId":"99"}`), nil
	case strings.HasSuffix(req.URL.Path, "/7/zip"):
		response := contractResponse(req, http.StatusFound, "")
		response.Header.Set("Location", tr.signedURL)

		return response, nil
	default:
		return contractResponse(req, http.StatusOK, `{"artifacts":[{"id":7,"name":"wanted"}]}`), nil
	}
}
