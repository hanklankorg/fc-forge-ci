// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/gitlab"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/fakegitserver"
)

const glUploadHash = "0123456789abcdef0123456789abcdef"

func retentionProvider(srv *fakegitserver.Server) *gitlab.Provider {
	return &gitlab.Provider{
		APIBaseOverride: srv.URL(),
		HTTPClient:      srv.Client(),
		Env:             envFunc(map[string]string{"GITLAB_TOKEN": "t"}),
	}
}

func TestListReleases_ReadsEachReleasesLinks(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.NewGitLab(t)
	srv.OnGet(glReleasesBase, func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `[{"tag_name":"v1.0.0","created_at":"2026-01-02T00:00:00Z"}]`}
	})
	srv.OnGet(glReleasesBase+"/"+glTag+"/assets/links", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `[{"id":7,"name":"app.tar.gz","url":"https://gitlab.invalid/itiquette/repo/uploads/` + glUploadHash + `/app.tar.gz"}]`}
	})

	releases, err := retentionProvider(srv).ListReleases(t.Context(), "itiquette/repo")
	if err != nil {
		t.Fatal(err)
	}

	if len(releases) != 1 || releases[0].Tag != glTag || releases[0].Created.Day() != 2 || len(releases[0].Assets) != 1 ||
		releases[0].Assets[0].ID != 7 || releases[0].Assets[0].Name != "app.tar.gz" {
		t.Errorf("releases = %+v", releases)
	}
}

// Deleting a release file deletes the upload behind its link, then the link:
// the link alone would keep the file's storage.
func TestDeleteReleaseAsset_DeletesTheUploadThenTheLink(t *testing.T) {
	t.Parallel()

	upload := "/api/v4/projects/" + glProject + "/uploads/" + glUploadHash + "/app.tar.gz"
	link := glReleasesBase + "/" + glTag + "/assets/links/7"

	srv := fakegitserver.NewGitLab(t)
	for _, path := range []string{upload, link} {
		srv.On(http.MethodDelete, path, func(fakegitserver.Request) fakegitserver.Response {
			return fakegitserver.Response{Status: 204}
		})
	}

	err := retentionProvider(srv).DeleteReleaseAsset(t.Context(), "itiquette/repo", provider.ListedRelease{Tag: glTag}, provider.ListedAsset{
		ID: 7, Name: "app.tar.gz", URL: "https://gitlab.invalid/-/project/42/uploads/" + glUploadHash + "/app.tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}

	requests := srv.Requests()
	deleted := make([]string, 0, len(requests))

	for _, req := range requests {
		deleted = append(deleted, req.Method+" "+req.Path)
	}

	if want := []string{"DELETE " + upload, "DELETE " + link}; !slices.Equal(deleted, want) {
		t.Errorf("requests = %v, want %v", deleted, want)
	}
}

// A link to anything but an upload of this project has no upload to delete:
// only the link goes.
func TestDeleteReleaseAsset_LeavesFilesOutsideTheProjectsUploads(t *testing.T) {
	t.Parallel()

	link := glReleasesBase + "/" + glTag + "/assets/links/8"

	for name, target := range map[string]string{
		"another host":      "https://elsewhere.invalid/itiquette/repo/uploads/" + glUploadHash + "/app.tar.gz",
		"a package":         "https://gitlab.invalid/api/v4/projects/1/packages/generic/app/1.0.0/app.tar.gz",
		"a short upload id": "https://gitlab.invalid/itiquette/repo/uploads/abc/app.tar.gz",
	} {
		srv := fakegitserver.NewGitLab(t)
		srv.On(http.MethodDelete, link, func(fakegitserver.Request) fakegitserver.Response {
			return fakegitserver.Response{Status: 204}
		})

		err := retentionProvider(srv).DeleteReleaseAsset(t.Context(), "itiquette/repo", provider.ListedRelease{Tag: glTag},
			provider.ListedAsset{ID: 8, Name: "app.tar.gz", URL: target})
		if requests := srv.Requests(); err != nil || len(requests) != 1 || requests[0].Path != link {
			t.Errorf("%s: %v, requests %+v; want only the link deleted", name, err, requests)
		}
	}
}

// Repointing a same-name link at a new upload deletes the upload it pointed
// at before; it used to stay in the project's storage with nothing linking it.
func TestUploadReleaseAsset_DeletesTheReplacedUpload(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.NewGitLab(t)
	asset := writeTestAsset(t, "artifact.tar.gz", "replacement")
	old := "/api/v4/projects/group%2Fproject/uploads/" + glUploadHash + "/artifact.tar.gz"

	srv.OnGet("/api/v4/projects/group%2Fproject/releases/v1.2.3/assets/links", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Body: `[{"id":42,"name":"artifact.tar.gz","url":"https://gitlab.invalid/group/project/uploads/` + glUploadHash + `/artifact.tar.gz"}]`}
	})
	srv.OnPut("/api/v4/projects/group%2Fproject/releases/v1.2.3/assets/links/42", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `{"id":42}`}
	})
	srv.OnPost("/api/v4/projects/group%2Fproject/uploads", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 201, Body: `{"full_path":"/group/project/uploads/fedcba9876543210fedcba9876543210/artifact.tar.gz"}`}
	})
	srv.On(http.MethodDelete, old, func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 204}
	})

	p := &gitlab.Provider{
		Env:             envFunc(map[string]string{"GITLAB_TOKEN": "glpat_test", "CI_PROJECT_PATH": "group/project"}),
		APIBaseOverride: srv.URL(),
		HTTPClient:      srv.Client(),
	}

	if err := p.UploadReleaseAsset(t.Context(), "v1.2.3", asset); err != nil {
		t.Fatal(err)
	}

	requests := srv.Requests()
	if last := requests[len(requests)-1]; last.Method != http.MethodDelete || last.Path != old {
		t.Errorf("last request = %s %s, want the replaced upload deleted after the link moved", last.Method, last.Path)
	}
}
