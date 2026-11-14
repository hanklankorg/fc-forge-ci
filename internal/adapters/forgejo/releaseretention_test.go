// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo_test

import (
	"net/http"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/stretchr/testify/require"
)

// Retention lists every release with all its attachments, and deletes one
// attachment by its release and id, leaving the release.
func TestReleaseRetention_ListsAndDeletesAttachments(t *testing.T) {
	t.Parallel()

	var calls []string

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.Method+" "+request.URL.Path)

		switch request.Method + " " + request.URL.Path {
		case "GET /api/v1/repos/examplescope/repo/releases":
			_, _ = response.Write([]byte(`[{"id":99,"tag_name":"v1.2.3","draft":false,"created_at":"2026-01-02T00:00:00Z"}]`))
		case "GET /api/v1/repos/examplescope/repo/releases/99/assets":
			_, _ = response.Write([]byte(`[{"id":1,"name":"app.tar.gz","size":1000,"browser_download_url":"https://forgejo.invalid/a"}]`))
		case "DELETE /api/v1/repos/examplescope/repo/releases/99/assets/1":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.Error(response, "unexpected "+request.Method+" "+request.URL.String(), http.StatusNotFound)
		}
	})

	forgejoProvider := newProvider(handler)

	releases, err := forgejoProvider.ListReleases(t.Context(), "examplescope/repo")
	require.NoError(t, err)
	require.Len(t, releases, 1)
	require.Equal(t, provider.ListedRelease{
		ID: 99, Tag: "v1.2.3", Created: releases[0].Created,
		Assets: []provider.ListedAsset{{ID: 1, Name: "app.tar.gz", Size: 1000, URL: "https://forgejo.invalid/a"}},
	}, releases[0])
	require.Equal(t, 2, releases[0].Created.Day())

	require.NoError(t, forgejoProvider.DeleteReleaseAsset(t.Context(), "examplescope/repo", releases[0], releases[0].Assets[0]))
	require.Equal(t, "DELETE /api/v1/repos/examplescope/repo/releases/99/assets/1", calls[len(calls)-1])
}

// The readback asks for one tag and that release's attachments; a tag
// without a release is none, not an error. A size of zero is reported as one.
func TestReadRelease_ReadsOnlyTheTaggedRelease(t *testing.T) {
	t.Parallel()

	var calls []string

	forgejoProvider := newProvider(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.Method+" "+request.URL.Path)

		switch request.URL.Path {
		case "/api/v1/repos/examplescope/repo/releases/tags/v1.2.3":
			_, _ = response.Write([]byte(`{"id":99,"tag_name":"v1.2.3","draft":true}`))
		case "/api/v1/repos/examplescope/repo/releases/99/assets":
			_, _ = response.Write([]byte(`[{"id":1,"name":"app.tar.gz","size":0}]`))
		default:
			http.Error(response, "not found", http.StatusNotFound)
		}
	}))

	release, err := forgejoProvider.ReadRelease(t.Context(), "examplescope/repo", "v1.2.3")
	require.NoError(t, err)
	require.Equal(t, &provider.ListedRelease{
		ID: 99, Tag: "v1.2.3", Draft: true, Created: release.Created, Assets: []provider.ListedAsset{{ID: 1, Name: "app.tar.gz"}},
	}, release)
	require.Equal(t, []string{
		"GET /api/v1/repos/examplescope/repo/releases/tags/v1.2.3", "GET /api/v1/repos/examplescope/repo/releases/99/assets",
	}, calls)

	release, err = forgejoProvider.ReadRelease(t.Context(), "examplescope/repo", "v9.9.9")
	require.NoError(t, err)
	require.Nil(t, release)
}
