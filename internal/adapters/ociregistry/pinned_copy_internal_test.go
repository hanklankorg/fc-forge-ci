// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // replaces and restores the library transport; every request stays in memory.
func TestCopyTag_TaggedDigestIgnoresAMovedTag(t *testing.T) {
	const (
		repository = "registry.invalid:5000/nested/team/app"
		candidate  = repository + ":staging-v1.2.3"
		dest       = repository + ":v1.2.3"
	)

	var (
		mu       sync.Mutex
		requests []string
	)

	handler := registry.New()
	original := remote.DefaultTransport

	t.Cleanup(func() { remote.DefaultTransport = original })

	remote.DefaultTransport = deleteRequestTransport(func(req *http.Request) (*http.Response, error) {
		mu.Lock()

		requests = append(requests, req.Method+" "+req.URL.Path)
		mu.Unlock()

		if req.Body != nil {
			defer func() { _ = req.Body.Close() }()
		}

		recorder := httptest.NewRecorder()

		serverRequest := req.Clone(req.Context())
		if serverRequest.Body == nil {
			serverRequest.Body = http.NoBody // net/http servers always provide a non-nil body.
		}

		handler.ServeHTTP(recorder, serverRequest)
		response := recorder.Result()
		response.Request = req

		return response, nil
	})

	first := empty.Image
	moved, err := mutate.ConfigFile(first, &v1.ConfigFile{Config: v1.Config{Labels: map[string]string{"fixture": "moved"}}})
	require.NoError(t, err)
	firstDigest, err := first.Digest()
	require.NoError(t, err)
	movedDigest, err := moved.Digest()
	require.NoError(t, err)
	require.NotEqual(t, firstDigest, movedDigest)

	options := []crane.Option{crane.WithContext(t.Context()), crane.WithAuth(authn.Anonymous)}
	require.NoError(t, crane.Push(first, candidate, options...))
	require.NoError(t, crane.Push(moved, candidate, options...))

	adapter := NewAnonymous()
	got, err := adapter.ResolveDigest(t.Context(), candidate)
	require.NoError(t, err)
	require.Equal(t, movedDigest.String(), got)

	mu.Lock()
	requests = nil
	mu.Unlock()

	require.NoError(t, adapter.CopyTag(t.Context(), candidate+"@"+firstDigest.String(), dest))
	got, err = adapter.ResolveDigest(t.Context(), dest)
	require.NoError(t, err)
	require.Equal(t, firstDigest.String(), got)

	mu.Lock()
	defer mu.Unlock()

	require.Contains(t, requests, "GET /v2/nested/team/app/manifests/"+firstDigest.String())
	require.NotContains(t, requests, "GET /v2/nested/team/app/manifests/staging-v1.2.3")
	require.Contains(t, requests, "PUT /v2/nested/team/app/manifests/v1.2.3")
}
