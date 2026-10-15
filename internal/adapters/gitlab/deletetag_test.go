// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/gitlab"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakegitserver"
)

const (
	glRegistryRepos = "/api/v4/projects/" + glProject + "/registry/repositories"
	glImageRef      = "registry.example.com/itiquette/repo:staging-v1.2.3"
)

func newDeleteTagProvider(srv *fakegitserver.Server) *gitlab.Provider {
	return &gitlab.Provider{
		APIBaseOverride: srv.URL(),
		HTTPClient:      srv.Client(),
		Env:             envFunc(map[string]string{"GITLAB_TOKEN": "t"}),
	}
}

func TestDeleteTag_DeletesTheTagKeepingTheManifest(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.NewGitLab(t)

	srv.OnGet(glRegistryRepos, func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `[{"id":42,"path":"itiquette/repo"}]`}
	})
	srv.On(http.MethodDelete, glRegistryRepos+"/42/tags/staging-v1.2.3", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `{}`}
	})

	if err := newDeleteTagProvider(srv).DeleteTag(context.Background(), glImageRef); err != nil {
		t.Fatalf("DeleteTag = %v", err)
	}

	// The tag endpoint, never the repository endpoint: deleting the repository
	// is asynchronous and would take the promoted manifest with it.
	if !hasCall(srv.Requests(), http.MethodDelete, glRegistryRepos+"/42/tags/staging-v1.2.3") {
		t.Error("expected a tag-scoped DELETE")
	}

	if hasCall(srv.Requests(), http.MethodDelete, glRegistryRepos+"/42") {
		t.Error("deleted the whole registry repository, which destroys the promoted image")
	}
}

// A registry repository nested below its project (group/project/image) resolves
// by trying the ref's path first and then the shorter project prefix.
func TestDeleteTag_ResolvesARepositoryNestedBelowItsProject(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.NewGitLab(t)

	srv.OnGet("/api/v4/projects/itiquette%2Frepo%2Fapp/registry/repositories",
		func(fakegitserver.Request) fakegitserver.Response {
			return fakegitserver.Response{Status: 404, Body: `{"message":"404 Project Not Found"}`}
		})
	srv.OnGet(glRegistryRepos, func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `[{"id":7,"path":"itiquette/repo/app"}]`}
	})
	srv.On(http.MethodDelete, glRegistryRepos+"/7/tags/staging-v1.2.3", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `{}`}
	})

	err := newDeleteTagProvider(srv).DeleteTag(context.Background(),
		"registry.example.com/itiquette/repo/app:staging-v1.2.3")
	if err != nil {
		t.Fatalf("DeleteTag = %v", err)
	}
}

// The guard that makes prefix-probing safe: a project may hold several registry
// repositories, and only the one whose path matches the ref may be touched.
func TestDeleteTag_RefusesToDeleteFromANeighbouringImage(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.NewGitLab(t)

	srv.OnGet(glRegistryRepos, func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `[{"id":9,"path":"itiquette/repo/other"}]`}
	})

	if err := newDeleteTagProvider(srv).DeleteTag(context.Background(), glImageRef); err != nil {
		t.Fatalf("DeleteTag = %v", err)
	}

	for _, req := range srv.Requests() {
		if req.Method == http.MethodDelete {
			t.Fatalf("deleted %s, but no registry repository matched the ref's path", req.Path)
		}
	}
}

func TestDeleteTag_TreatsAnAbsentTagAsAlreadyDeleted(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.NewGitLab(t)

	srv.OnGet(glRegistryRepos, func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `[{"id":42,"path":"itiquette/repo"}]`}
	})
	srv.On(http.MethodDelete, glRegistryRepos+"/42/tags/staging-v1.2.3", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 404, Body: `{"message":"404 Tag Not Found"}`}
	})

	if err := newDeleteTagProvider(srv).DeleteTag(context.Background(), glImageRef); err != nil {
		t.Fatalf("DeleteTag on a missing tag should be idempotent, got %v", err)
	}
}

// Only a 404 means the tag is already gone. A refused delete is a failure: read
// as success, a cleanup would report a tag removed that is still served.
func TestDeleteTag_ReportsARefusedDelete(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.NewGitLab(t)

	srv.OnGet(glRegistryRepos, func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 200, Body: `[{"id":42,"path":"itiquette/repo"}]`}
	})
	srv.On(http.MethodDelete, glRegistryRepos+"/42/tags/staging-v1.2.3", func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 403, Body: `{"message":"403 Forbidden"}`}
	})

	err := newDeleteTagProvider(srv).DeleteTag(t.Context(), glImageRef)
	if !errors.Is(err, errs.ErrPermissionDenied) {
		t.Fatalf("a refused delete = %v, want ErrPermissionDenied", err)
	}
}

func TestDeleteTag_IsSilentWhenTheRepositoryDoesNotExist(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.NewGitLab(t)

	srv.OnGet(glRegistryRepos, func(fakegitserver.Request) fakegitserver.Response {
		return fakegitserver.Response{Status: 404, Body: `{"message":"404 Project Not Found"}`}
	})

	if err := newDeleteTagProvider(srv).DeleteTag(context.Background(), glImageRef); err != nil {
		t.Fatalf("DeleteTag on an absent repository should be idempotent, got %v", err)
	}
}

// The refusal that protects the shared-digest promotion model: a digest names a
// manifest, and deleting it would destroy the promoted release image.
func TestDeleteTag_RefusesADigestPinnedRef(t *testing.T) {
	t.Parallel()

	srv := fakegitserver.NewGitLab(t)

	err := newDeleteTagProvider(srv).DeleteTag(context.Background(),
		"registry.example.com/itiquette/repo@sha256:"+strings.Repeat("a", 64))

	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("want ErrUsage for a digest-pinned ref, got %v", err)
	}

	if len(srv.Requests()) != 0 {
		t.Error("a digest-pinned ref reached the server; it must be refused before any call")
	}
}
