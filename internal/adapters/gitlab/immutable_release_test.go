// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab_test

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

	"github.com/diggsweden/forge-ci/internal/adapters/gitlab"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestNumberedPrereleasePublishesCompleteSetAndRetriesReadOnly(t *testing.T) {
	var (
		published map[string]any
		assetBody []byte
	)

	mutations := 0

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			mutations++
		}

		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/releases/v1.0.0-beta.2"):
			if published == nil {
				http.NotFound(response, request)

				return
			}

			_ = json.NewEncoder(response).Encode(published)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/uploads"):
			file, _, err := request.FormFile("file")
			if err != nil {
				t.Error(err)
				response.WriteHeader(http.StatusInternalServerError)

				return
			}
			defer func() { _ = file.Close() }()

			assetBody, err = io.ReadAll(file)
			if err != nil {
				t.Error(err)
			}

			_ = json.NewEncoder(response).Encode(map[string]string{"full_path": "/owner/repo/uploads/" + strings.Repeat("a", 32) + "/app.tgz"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/releases"):
			if len(assetBody) == 0 {
				t.Error("release became visible before assets uploaded")
			}

			if err := json.NewDecoder(request.Body).Decode(&published); err != nil {
				t.Error(err)
			}

			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(published)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/assets/links"):
			assets := published["assets"].(map[string]any)
			_ = json.NewEncoder(response).Encode(assets["links"])
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/v4/projects/") && strings.HasSuffix(request.URL.Path, "/uploads/"+strings.Repeat("a", 32)+"/app.tgz"):
			if request.Header.Get("PRIVATE-TOKEN") != "fixture-token" {
				t.Error("private upload verification lacks its API credential")
			}

			_, _ = response.Write(assetBody)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
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

	gitlabProvider := &gitlab.Provider{APIBaseOverride: server.URL, HTTPClient: server.Client(), Env: envFunc(map[string]string{"GITLAB_TOKEN": "fixture-token"})}

	spec := provider.ReleaseSpec{Immutable: true, Tag: "v1.0.0-beta.2", Name: "Beta", NotesFile: notes, Prerelease: true, Assets: []string{asset}}
	if err := gitlabProvider.PublishRelease(t.Context(), "owner/repo", spec); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(published["description"].(string), "Pre-release") {
		t.Fatal("GitLab prerelease classification missing")
	}

	mutations = 0

	if err := gitlabProvider.PublishRelease(t.Context(), "owner/repo", spec); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(asset, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := gitlabProvider.PublishRelease(t.Context(), "owner/repo", spec); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("changed beta accepted: %v", err)
	}

	if mutations != 0 {
		t.Fatalf("published beta mutated %d times", mutations)
	}
}
