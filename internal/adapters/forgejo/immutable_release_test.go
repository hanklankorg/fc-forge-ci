// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/forgejo"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestNumberedPrereleaseRetryVerifiesBytesWithoutMutation(t *testing.T) {
	mutations := 0

	var server *httptest.Server

	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			mutations++

			response.WriteHeader(http.StatusInternalServerError)

			return
		}

		switch {
		case strings.HasSuffix(request.URL.Path, "/releases/tags/v1.0.0-beta.2"):
			_ = json.NewEncoder(response).Encode(map[string]any{"id": 1, "tag_name": "v1.0.0-beta.2", "name": "Beta", "body": "notes", "prerelease": true})
		case strings.HasSuffix(request.URL.Path, "/releases/1/assets"):
			_ = json.NewEncoder(response).Encode([]map[string]any{{"id": 2, "name": "app.tgz", "size": 8, "browser_download_url": server.URL + "/asset"}})
		case request.URL.Path == "/asset":
			_, _ = response.Write([]byte("artifact"))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	dir := t.TempDir()

	asset, notes := filepath.Join(dir, "app.tgz"), filepath.Join(dir, "notes.md")
	if err := os.WriteFile(asset, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(notes, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	forgejoProvider := &forgejo.Provider{APIBaseOverride: server.URL, HTTPClient: server.Client(), Env: func(string) string { return "" }}

	spec := provider.ReleaseSpec{Immutable: true, Tag: "v1.0.0-beta.2", Name: "Beta", NotesFile: notes, Prerelease: true, Assets: []string{asset}}
	if err := forgejoProvider.PublishRelease(t.Context(), "owner/repo", spec); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(asset, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := forgejoProvider.PublishRelease(t.Context(), "owner/repo", spec); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("changed beta accepted: %v", err)
	}

	if mutations != 0 {
		t.Fatalf("published beta mutated %d times", mutations)
	}
}

func TestImmutableReleaseCannotDeleteAPublicationSeenOnSecondLookup(t *testing.T) {
	lookups, mutations := 0, 0

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			mutations++

			response.WriteHeader(http.StatusInternalServerError)

			return
		}

		lookups++
		if lookups == 1 {
			http.NotFound(response, request)

			return
		}

		_ = json.NewEncoder(response).Encode(map[string]any{"id": 1, "tag_name": "v1.0.0-beta.2", "name": "Beta", "body": "Beta", "prerelease": true, "draft": false})
	}))
	defer server.Close()

	forgejoProvider := &forgejo.Provider{APIBaseOverride: server.URL, HTTPClient: server.Client(), Env: func(string) string { return "" }}

	err := forgejoProvider.CreateRelease(t.Context(), "owner/repo", provider.ReleaseSpec{Immutable: true, Tag: "v1.0.0-beta.2", Name: "Beta", Prerelease: true})
	if !errors.Is(err, errs.ErrValidation) || mutations != 0 {
		t.Fatalf("published release reached mutation path: error=%v mutations=%d", err, mutations)
	}
}
