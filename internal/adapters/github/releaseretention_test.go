// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github_test

import (
	"net/http"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/github"
	"github.com/diggsweden/forge-ci/internal/testutil/fakegitserver"
)

// Retention lists every release with its assets, reports GitHub's immutable
// flag, and deletes one asset by id.
func TestReleaseRetention_ListsAndDeletesAssets(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.New(t)
	srv.OnGet("/repos/owner/repo/releases", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `[{"id":5,"tag_name":"v1.0.0","immutable":true,"created_at":"2026-01-02T00:00:00Z"}]`}
	})
	srv.OnGet("/repos/owner/repo/releases/5/assets", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `[{"id":9,"name":"app.tar.gz","size":1000}]`}
	})
	srv.On(http.MethodDelete, "/repos/owner/repo/releases/assets/9", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 204}
	})

	p := &github.Provider{
		APIBaseOverride: srv.URL(), HTTPClient: srv.Client(),
		Env: envFunc(map[string]string{"GITHUB_TOKEN": "test-token"}),
	}

	releases, err := p.ListReleases(t.Context(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}

	if len(releases) != 1 || releases[0].ID != 5 || !releases[0].Immutable || releases[0].Created.Day() != 2 ||
		len(releases[0].Assets) != 1 || releases[0].Assets[0].ID != 9 || releases[0].Assets[0].Size != 1000 {
		t.Fatalf("releases = %+v", releases)
	}

	if err := p.DeleteReleaseAsset(t.Context(), "owner/repo", releases[0], releases[0].Assets[0]); err != nil {
		t.Fatal(err)
	}

	requests := srv.Requests()
	if last := requests[len(requests)-1]; last.Method != http.MethodDelete || last.Path != "/repos/owner/repo/releases/assets/9" {
		t.Errorf("last request = %s %s", last.Method, last.Path)
	}
}
