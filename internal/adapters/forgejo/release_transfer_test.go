// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/forgejo"
	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// transferCap stands in for the 30s API client cap; FORGE_CI_HTTP_TIMEOUT
// sets the same value as the transfer's idle window.
const transferCap = 150 * time.Millisecond

// attachmentServer is release 99 with no attachments. upload answers the
// attachment POST.
func attachmentServer(t *testing.T, upload http.HandlerFunc) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/repos/examplescope/repo/releases/tags/v1.2.3":
			_, _ = response.Write([]byte(`{"id":99,"tag_name":"v1.2.3"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/repos/examplescope/repo/releases/99/assets":
			_, _ = response.Write([]byte(`[]`))
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/repos/examplescope/repo/releases/99/assets":
			upload(response, request)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func uploadAsset(t *testing.T, srv *httptest.Server, transport http.RoundTripper) error {
	t.Helper()

	asset := filepath.Join(t.TempDir(), "asset.tgz")
	if err := os.WriteFile(asset, []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &forgejo.Provider{
		Env:             envMap(map[string]string{"FORGEJO_TOKEN": "tok", "FORGEJO_REPOSITORY": "examplescope/repo"}),
		HTTPClient:      httpretry.NewClient(transferCap, transport),
		APIBaseOverride: srv.URL,
	}

	return p.UploadReleaseAsset(t.Context(), "v1.2.3", asset)
}

// A release attachment larger than the link moves within the API client's
// cap must still arrive whole while it keeps moving.
func TestUploadReleaseAsset_AnAttachmentThatKeepsMovingOutlivesTheClientCap(t *testing.T) {
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", transferCap.String())

	var received []byte

	srv := attachmentServer(t, func(response http.ResponseWriter, request *http.Request) {
		received, _ = io.ReadAll(request.Body)

		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"id":7,"name":"asset.tgz"}`))
	})

	// The multipart body of a 64-byte file goes in 32-byte pieces, 40ms
	// apart: well past the 150ms cap.
	slow := slowLink{next: srv.Client().Transport, gap: 40 * time.Millisecond, method: http.MethodPost, chunk: 32}

	if err := uploadAsset(t, srv, slow); err != nil {
		t.Fatalf("slow attachment: %v", err)
	}

	if !strings.Contains(string(received), strings.Repeat("a", 64)) {
		t.Fatalf("server received %q, want the whole asset", received)
	}
}

func TestUploadReleaseAsset_AStalledAttachmentSaysItStalled(t *testing.T) {
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", transferCap.String())

	// The server takes the whole body and never answers. Reading the body
	// first lets it see the client hang up.
	srv := attachmentServer(t, func(_ http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		<-request.Context().Done()
	})

	if err := uploadAsset(t, srv, srv.Client().Transport); !errors.Is(err, httpretry.ErrStalled) {
		t.Fatalf("stalled attachment: err = %v, want ErrStalled", err)
	}
}

// publishedAssetServer is a published immutable release whose one asset,
// "artifact", asset serves.
func publishedAssetServer(t *testing.T, asset http.HandlerFunc) *httptest.Server {
	t.Helper()

	var srv *httptest.Server

	srv = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method != http.MethodGet:
			t.Errorf("published release mutated: %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(request.URL.Path, "/releases/tags/v1.0.0"):
			_ = json.NewEncoder(response).Encode(map[string]any{"id": 1, "tag_name": "v1.0.0", "name": "One", "body": "One"})
		case strings.HasSuffix(request.URL.Path, "/releases/1/assets"):
			_ = json.NewEncoder(response).Encode([]map[string]any{{"id": 2, "name": "app.tgz", "size": 8, "browser_download_url": srv.URL + "/asset"}})
		case request.URL.Path == "/asset":
			asset(response, request)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func verifyPublished(t *testing.T, srv *httptest.Server) error {
	t.Helper()

	asset := filepath.Join(t.TempDir(), "app.tgz")
	if err := os.WriteFile(asset, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &forgejo.Provider{APIBaseOverride: srv.URL, HTTPClient: httpretry.NewClient(transferCap, srv.Client().Transport), Env: func(string) string { return "" }}

	return p.PublishRelease(t.Context(), "owner/repo", provider.ReleaseSpec{Immutable: true, Tag: "v1.0.0", Name: "One", Assets: []string{asset}})
}

// Checking a published immutable release reads each asset back; a large one
// must not be cut off by the API client's cap while it keeps moving.
func TestPublishRelease_APublishedAssetThatKeepsMovingOutlivesTheClientCap(t *testing.T) {
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", transferCap.String())

	srv := publishedAssetServer(t, func(response http.ResponseWriter, _ *http.Request) {
		flusher, _ := response.(http.Flusher)

		for _, b := range []byte("artifact") {
			_, _ = response.Write([]byte{b})

			flusher.Flush()
			time.Sleep(40 * time.Millisecond)
		}
	})

	if err := verifyPublished(t, srv); err != nil {
		t.Fatalf("slow published asset: %v", err)
	}
}

func TestPublishRelease_AStalledPublishedAssetSaysItStalled(t *testing.T) {
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", transferCap.String())

	srv := publishedAssetServer(t, func(response http.ResponseWriter, request *http.Request) {
		flusher, _ := response.(http.Flusher)

		_, _ = response.Write([]byte("a"))

		flusher.Flush()
		<-request.Context().Done()
	})

	if err := verifyPublished(t, srv); !errors.Is(err, httpretry.ErrStalled) {
		t.Fatalf("stalled published asset: err = %v, want ErrStalled", err)
	}
}
