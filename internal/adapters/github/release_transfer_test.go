// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github_test

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
)

// releaseCap stands in for the 30s API client cap. The progressing upload's
// idle budget is independent, so parallel compiler load is not a fake stall.
const releaseCap = 150 * time.Millisecond

// slowUpload takes POST bodies 32 bytes per gap before handing the request
// on, as a slow uplink does; a kernel socket buffer would swallow a small
// test body at once.
type slowUpload struct {
	next http.RoundTripper
	gap  time.Duration
}

func (s slowUpload) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.Body == nil || req.Body == http.NoBody {
		return s.next.RoundTrip(req)
	}

	var sent []byte

	piece := make([]byte, 32)

	for {
		n, err := req.Body.Read(piece)
		sent = append(sent, piece[:n]...)

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		select {
		case <-req.Context().Done():
			return nil, context.Cause(req.Context())
		case <-time.After(s.gap):
		}
	}

	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(sent))

	return s.next.RoundTrip(out)
}

func uploadReleaseAsset(t *testing.T, handler http.Handler, transport func(http.RoundTripper) http.RoundTripper) error {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	asset := filepath.Join(t.TempDir(), "binary.tgz")
	if err := os.WriteFile(asset, []byte(strings.Repeat("b", 256)), 0o600); err != nil {
		t.Fatal(err)
	}

	p := providerForFake(srv)
	p.HTTPClient = httpretry.NewClient(releaseCap, transport(srv.Client().Transport))

	return p.UploadReleaseAsset(t.Context(), "v1.0.0", asset)
}

// A release asset larger than the link moves within the API client's cap
// must still arrive whole while it keeps moving.
func TestUploadReleaseAsset_AnAssetThatKeepsMovingOutlivesTheClientCap(t *testing.T) {
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", (2 * time.Second).String())

	fake := newFake()
	fake.preloadRelease(false, false)

	// 256 bytes in 32-byte pieces, 40ms apart: well past the 150ms cap.
	slow := func(next http.RoundTripper) http.RoundTripper {
		return slowUpload{next: next, gap: 40 * time.Millisecond}
	}

	if err := uploadReleaseAsset(t, combinedHandler(fake), slow); err != nil {
		t.Fatalf("slow asset: %v", err)
	}
}

func TestUploadReleaseAsset_AStalledAssetSaysItStalled(t *testing.T) {
	t.Setenv("FORGE_CI_HTTP_TIMEOUT", releaseCap.String())

	fake := newFake()
	fake.preloadRelease(false, false)

	api := combinedHandler(fake)

	// The upload host takes the whole body and never answers. Reading the
	// body first lets it see the client hang up.
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			api.ServeHTTP(response, request)

			return
		}

		_, _ = io.Copy(io.Discard, request.Body)
		<-request.Context().Done()
	})

	same := func(next http.RoundTripper) http.RoundTripper { return next }

	if err := uploadReleaseAsset(t, handler, same); !errors.Is(err, httpretry.ErrStalled) {
		t.Fatalf("stalled asset: err = %v, want ErrStalled", err)
	}
}
