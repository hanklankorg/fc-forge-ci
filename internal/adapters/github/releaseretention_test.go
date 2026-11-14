// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/github"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
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

	provider := &github.Provider{
		APIBaseOverride: srv.URL(), HTTPClient: srv.Client(),
		Env: envFunc(map[string]string{"GITHUB_TOKEN": "test-token"}),
	}

	releases, err := provider.ListReleases(t.Context(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}

	if len(releases) != 1 || releases[0].ID != 5 || !releases[0].Immutable || releases[0].Created.Day() != 2 ||
		len(releases[0].Assets) != 1 || releases[0].Assets[0].ID != 9 || releases[0].Assets[0].Size != 1000 {
		t.Fatalf("releases = %+v", releases)
	}

	if err := provider.DeleteReleaseAsset(t.Context(), "owner/repo", releases[0], releases[0].Assets[0]); err != nil {
		t.Fatal(err)
	}

	requests := srv.Requests()
	if last := requests[len(requests)-1]; last.Method != http.MethodDelete || last.Path != "/repos/owner/repo/releases/assets/9" {
		t.Errorf("last request = %s %s", last.Method, last.Path)
	}
}

// The readback asks for one tag and that release's assets, never the other
// releases'. The tag endpoint omits drafts, so a tag it does not know is
// looked up in the release list: a visible draft is found, two releases on
// the tag are refused, and a tag with no release is none.
func TestReadRelease_ReadsOnlyTheTaggedRelease(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		byTag, list string
		wantAssets  int
		wantDraft   bool
		wantNone    bool
		wantErr     error
	}{
		"published":      {byTag: `{"id":5,"tag_name":"v1.0.0"}`, wantAssets: 1},
		"a draft":        {list: `[{"id":4,"tag_name":"v0.9.0"},{"id":5,"tag_name":"v1.0.0","draft":true}]`, wantAssets: 1, wantDraft: true},
		"two on the tag": {list: `[{"id":5,"tag_name":"v1.0.0","draft":true},{"id":6,"tag_name":"v1.0.0","draft":true}]`, wantErr: errs.ErrValidation},
		"none":           {list: `[{"id":4,"tag_name":"v0.9.0"}]`, wantNone: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := fakegitserver.New(t)
			srv.OnGet("/repos/owner/repo/releases/tags/v1.0.0", func(fakegitserver.Request) fakegitserver.Response {
				if tc.byTag == "" {
					return fakegitserver.Response{Status: 404, Body: `{"message":"Not Found"}`}
				}

				return fakegitserver.Response{Status: 200, Body: tc.byTag}
			})
			srv.OnGet("/repos/owner/repo/releases", func(fakegitserver.Request) fakegitserver.Response {
				return fakegitserver.Response{Status: 200, Body: tc.list}
			})
			srv.OnGet("/repos/owner/repo/releases/5/assets", func(fakegitserver.Request) fakegitserver.Response {
				return fakegitserver.Response{Status: 200, Body: `[{"id":9,"name":"app.tar.gz","size":0,"digest":"sha256:ab"}]`}
			})

			release, err := (&github.Provider{
				APIBaseOverride: srv.URL(), HTTPClient: srv.Client(),
				Env: envFunc(map[string]string{"GITHUB_TOKEN": "test-token"}),
			}).ReadRelease(t.Context(), "owner/repo", "v1.0.0")

			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			case tc.wantNone:
				if release != nil {
					t.Fatalf("release = %+v, want none", release)
				}
			case release == nil || release.ID != 5 || release.Draft != tc.wantDraft || len(release.Assets) != tc.wantAssets ||
				release.Assets[0].Name != "app.tar.gz" || release.Assets[0].SizeUnknown ||
				release.Assets[0].Digest != "sha256:ab" || !release.Assets[0].DigestExpected:
				t.Fatalf("release = %+v", release)
			}

			for _, request := range srv.Requests() {
				if request.Path == "/repos/owner/repo/releases/4/assets" || (tc.byTag != "" && request.Path == "/repos/owner/repo/releases") {
					t.Errorf("read more than the tagged release: %s", request.Path)
				}
			}
		})
	}
}
