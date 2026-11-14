// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/forgejo"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

type artifactFailureTransport struct {
	base http.RoundTripper
}

func (transport artifactFailureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/file" {
		return nil, io.ErrUnexpectedEOF
	}

	return transport.base.RoundTrip(req)
}

func TestDownloadRunArtifact_TransportFailureDoesNotExposeSignedURL(t *testing.T) {
	t.Parallel()

	const signature = "private-download-signature"

	srv := serveRuntime(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/container" {
			writeJSON(w, jsonObj{"value": []jsonObj{{"path": "dist/sbom.json", "itemType": "file", "contentLocation": runtimeFakeURL + "/file?signature=" + signature}}})
		} else {
			writeJSON(w, jsonObj{"value": []jsonObj{{"name": "dist", "fileContainerResourceUrl": runtimeFakeURL + "/container"}}})
		}
	}))
	srv.client.Transport = artifactFailureTransport{base: srv.client.Transport}

	_, err := runtimeProvider(srv).DownloadRunArtifact(t.Context(), provider.RunArtifactDownload{Name: "dist", Dir: t.TempDir()})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("transport cause must be preserved: %v", err)
	}

	if !strings.Contains(err.Error(), `entry "sbom.json"`) {
		t.Errorf("missing artifact file context: %v", err)
	}

	for _, private := range []string{signature, runtimeFakeURL} {
		if strings.Contains(err.Error(), private) {
			t.Errorf("transport error exposed its requested URL: %v", err)
		}
	}
}

func TestDownloadRunArtifact_EntryFailureNamesFileWithoutResponseSecrets(t *testing.T) {
	t.Parallel()

	const signedQuery = "private-signed-download-value"

	srv := serveRuntime(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_apis/pipelines/workflows/7/artifacts":
			writeJSON(response, jsonObj{"value": []jsonObj{{"name": "runtime-evidence-full", "fileContainerResourceUrl": runtimeFakeURL + "/container"}}})
		case "/container":
			writeJSON(response, jsonObj{"value": []jsonObj{{"path": "runtime-evidence-full/sbom-arm64.json", "itemType": "file", "contentLocation": runtimeFakeURL + "/file?signature=" + signedQuery}}})
		case "/file":
			http.Error(response, "storage failure: "+request.Header.Get("Authorization")+" "+signedQuery, http.StatusInternalServerError)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))

	_, err := runtimeProvider(srv).DownloadRunArtifact(t.Context(), provider.RunArtifactDownload{Name: "runtime-evidence-full", Dir: t.TempDir()})
	if !errors.Is(err, errs.ErrDependencyUnavailable) {
		t.Fatalf("expected dependency-unavailable classification, got %v", err)
	}

	for _, want := range []string{`artifact "runtime-evidence-full"`, `entry "sbom-arm64.json"`, "HTTP 500"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing diagnostic %q in %v", want, err)
		}
	}

	for _, secret := range []string{runtimeToken, signedQuery, runtimeFakeURL} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error exposed response or URL data: %v", err)
		}
	}
}

// TestDownloadRunArtifact_FailsLoudWithoutLeakingToken locks two disciplines on
// the token-bearing HTTP adapter at once: it must FAIL LOUD on a non-2xx (no
// swallowed error) and must never leak the runtime Bearer token into the error
// it surfaces — the token lives only in the request header. A future refactor
// that interpolates the response or request into the error would trip this.
func TestDownloadRunArtifact_FailsLoudWithoutLeakingToken(t *testing.T) {
	t.Parallel()

	const secretToken = "super-secret-runtime-token-do-not-log"

	handler := http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
		// Auth must still be carried (proves the token reaches the wire)…
		if r.Header.Get("Authorization") != "Bearer "+secretToken {
			response.WriteHeader(http.StatusUnauthorized)

			return
		}
		// …then fail server-side so the adapter must surface an error.
		response.WriteHeader(http.StatusInternalServerError)
	})

	prov := &forgejo.Provider{
		Env: envMap(map[string]string{
			"ACTIONS_RUNTIME_URL":   runtimeFakeURL,
			"ACTIONS_RUNTIME_TOKEN": secretToken,
			"FORGEJO_RUN_ID":        "7",
		}),
		HTTPClient: inMemoryClient(handler),
	}

	_, err := prov.DownloadRunArtifact(context.Background(),
		provider.RunArtifactDownload{Name: "dist", Dir: t.TempDir()})
	if err == nil {
		t.Fatal("expected a fail-loud error on HTTP 500, got nil")
	}

	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should name the HTTP status (fail loud); got: %v", err)
	}

	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("error leaked the runtime Bearer token: %v", err)
	}
}
