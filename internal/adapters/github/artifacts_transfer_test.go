// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/github"
	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// slowBlob hands the artifact zip's body over in small pieces, one per gap,
// as a slow link does; every other answer passes untouched.
type slowBlob struct {
	next http.RoundTripper
	gap  time.Duration
}

func (s slowBlob) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := s.next.RoundTrip(req)
	if err != nil || req.URL.Path != "/blob/42" {
		return resp, err
	}

	resp.Body = &slowBody{body: resp.Body, done: req.Context().Done(), gap: s.gap}

	return resp, nil
}

type slowBody struct {
	body io.ReadCloser
	done <-chan struct{}
	gap  time.Duration
}

func (b *slowBody) Read(p []byte) (int, error) {
	select {
	case <-b.done:
		return 0, context.Canceled
	case <-time.After(b.gap):
	}

	return b.body.Read(p[:min(len(p), 16)])
}

func (b *slowBody) Close() error { return b.body.Close() }

// An artifact zip larger than the link moves within the API client's cap
// must still arrive whole while it keeps moving. The client is built the way
// the production one is, with a 200ms cap in place of 30s.
func TestProvider_DownloadRunArtifact_AZipThatKeepsMovingOutlivesTheClientTimeout(t *testing.T) {
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", "200ms")

	zipBody := buildZip(t, map[string]string{"hello.txt": "world", "nested/inside.json": `{"ok":true}`})
	srv := serveArtifactZip(t, zipBody)

	const limit = 200 * time.Millisecond

	client := httpretry.NewClient(limit, slowBlob{next: srv.Client().Transport, gap: 40 * time.Millisecond})

	if pieces := len(zipBody)/16 + 1; time.Duration(pieces)*40*time.Millisecond <= limit {
		t.Fatalf("the %d-byte zip moves in %d pieces, inside the client Timeout; the test no longer shows the fault", len(zipBody), pieces)
	}

	p := &github.Provider{
		Env:             envFunc(map[string]string{"GITHUB_REPOSITORY": "owner/repo"}),
		APIBaseOverride: srv.URL(), HTTPClient: client,
	}

	dir := t.TempDir()

	if _, err := p.DownloadRunArtifact(context.Background(), provider.RunArtifactDownload{
		Name: "build-artifacts", Dir: dir, RunID: "9876",
	}); err != nil {
		t.Fatalf("slow zip: %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(dir, "hello.txt")); err != nil || string(got) != "world" {
		t.Fatalf("hello.txt = %q, err %v", got, err)
	}
}
