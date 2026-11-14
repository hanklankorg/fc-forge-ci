// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// slowRuntime serves one artifact entry of size bytes, one byte per gap, so
// it outlasts a short client Timeout while it keeps moving, and records an
// upload.
func slowRuntime(t *testing.T, size int, gap time.Duration) (*httptest.Server, *[]byte) {
	t.Helper()

	var uploaded []byte

	var srv *httptest.Server

	srv = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/_apis/pipelines/workflows/7/artifacts" && req.Method == http.MethodGet:
			writeJSON(response, jsonObj{"value": []jsonObj{{"name": artifactName, "fileContainerResourceUrl": srv.URL + "/container"}}})
		case req.URL.Path == "/_apis/pipelines/workflows/7/artifacts" && req.Method == http.MethodPost:
			writeJSON(response, jsonObj{"fileContainerResourceUrl": srv.URL + "/container"})
		case req.URL.Path == "/_apis/pipelines/workflows/7/artifacts" && req.Method == http.MethodPatch:
			response.WriteHeader(http.StatusOK)
		case req.URL.Path == "/container" && req.Method == http.MethodGet:
			writeJSON(response, jsonObj{"value": []jsonObj{{
				"path": "dist/big", "itemType": "file", "contentLocation": srv.URL + "/file", "fileLength": size,
			}}})
		case req.URL.Path == "/container" && req.Method == http.MethodPut:
			uploaded, _ = io.ReadAll(req.Body)

			response.WriteHeader(http.StatusOK)
		case req.URL.Path == "/file":
			flusher, _ := response.(http.Flusher)

			for range size {
				_, _ = response.Write([]byte{'x'})

				flusher.Flush()
				time.Sleep(gap)
			}
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, &uploaded
}

// slowLink sends the bodies of method requests chunk bytes per gap before
// handing the request on, as a slow uplink takes them; a kernel socket buffer
// would swallow a small test body at once.
type slowLink struct {
	next   http.RoundTripper
	gap    time.Duration
	method string
	chunk  int
}

func (l slowLink) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != l.method || req.Body == nil || req.Body == http.NoBody {
		return l.next.RoundTrip(req)
	}

	var sent []byte

	one := make([]byte, max(l.chunk, 1))

	for {
		n, err := req.Body.Read(one)
		sent = append(sent, one[:n]...)

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		select {
		case <-req.Context().Done():
			return nil, context.Cause(req.Context())
		case <-time.After(l.gap):
		}
	}

	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(sent))

	return l.next.RoundTrip(out)
}

// An artifact entry larger than the link moves within the API client's cap
// must still arrive whole while it keeps moving: live, a large OCI layout
// blob was cut off after 30 seconds. The client is built the way the
// production one is, with a 150ms cap in place of 30s.
func TestDownloadRunArtifact_AnEntryThatKeepsMovingOutlivesTheClientTimeout(t *testing.T) {
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", "150ms")

	srv, _ := slowRuntime(t, 10, 40*time.Millisecond)
	client := httpretry.NewClient(150*time.Millisecond, srv.Client().Transport)

	p := runtimeProvider(runtimeService{URL: srv.URL, client: client})
	dir := t.TempDir()

	info, err := p.DownloadRunArtifact(context.Background(), provider.RunArtifactDownload{Name: artifactName, Dir: dir})
	if err != nil {
		t.Fatalf("slow entry: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "big"))
	if err != nil || info.Bytes != 10 || string(got) != strings.Repeat("x", 10) {
		t.Fatalf("entry = %q (%d bytes), err %v; want 10 bytes", got, info.Bytes, err)
	}
}

func TestUploadRunArtifact_AFileThatKeepsMovingOutlivesTheClientTimeout(t *testing.T) {
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", "150ms")

	srv, uploaded := slowRuntime(t, 0, 0)
	client := httpretry.NewClient(150*time.Millisecond, slowLink{next: srv.Client().Transport, gap: 40 * time.Millisecond, method: http.MethodPut, chunk: 1})

	dir := t.TempDir()
	path := filepath.Join(dir, "big")

	if err := os.WriteFile(path, []byte(strings.Repeat("y", 10)), 0o600); err != nil {
		t.Fatal(err)
	}

	p := runtimeProvider(runtimeService{URL: srv.URL, client: client})

	if _, err := p.UploadRunArtifact(context.Background(), provider.RunArtifactUpload{Name: artifactName, Files: []string{path}}); err != nil {
		t.Fatalf("upload: %v", err)
	}

	if string(*uploaded) != strings.Repeat("y", 10) {
		t.Fatalf("uploaded %q", *uploaded)
	}
}
