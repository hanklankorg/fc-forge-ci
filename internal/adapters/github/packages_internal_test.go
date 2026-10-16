// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func versionJSON(id int, digestChar byte, tags ...string) string {
	quoted := make([]string, 0, len(tags))
	for _, tag := range tags {
		quoted = append(quoted, fmt.Sprintf("%q", tag))
	}

	return fmt.Sprintf(`{"id":%d,"name":"sha256:%s","created_at":"2026-10-0%dT00:00:00Z","metadata":{"package_type":"container","container":{"tags":[%s]}}}`,
		id, strings.Repeat(string(digestChar), 64), 1+id%9, strings.Join(quoted, ","))
}

// packageServer serves one organization package's versions and records the
// versions deleted. deleteStatus is the answer to every DELETE.
type packageServer struct {
	mu           sync.Mutex
	deleted      []string
	deleteStatus int
}

func (s *packageServer) start(t *testing.T, versionsPath, body string) *Packages {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == versionsPath:
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.EscapedPath(), versionsPath+"/"):
			s.mu.Lock()
			s.deleted = append(s.deleted, strings.TrimPrefix(r.URL.EscapedPath(), versionsPath+"/"))
			s.mu.Unlock()
			w.WriteHeader(s.deleteStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(server.Close)

	packages, err := NewPackages(server.Client(), "token")
	if err != nil {
		t.Fatal(err)
	}

	// Only this package's inert fixture replaces the fixed production authority.
	packages.client.BaseURL, err = url.Parse(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}

	return packages
}

func TestPackages_UsesGitHubAuthorityOnOtherRunners(t *testing.T) {
	t.Setenv("GITHUB_API_URL", "https://forgejo.invalid/api/v1")
	t.Setenv("GITHUB_SERVER_URL", "https://forgejo.invalid")

	var requests []string

	client := &http.Client{Transport: contractTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme+"://"+req.URL.Host != "https://api.github.com" {
			t.Errorf("GitHub Packages credential routed to %s", req.URL)
		}

		if req.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("the explicit package credential did not reach the GitHub API")
		}

		requests = append(requests, req.Method+" "+req.URL.Path)
		if req.Method == http.MethodDelete {
			return contractResponse(req, http.StatusNoContent, ""), nil
		}

		return contractResponse(req, http.StatusOK, "[]"), nil
	})}

	packages, err := NewPackages(client, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := packages.PackageVersions(t.Context(), "acme", "app"); err != nil {
		t.Fatal(err)
	}

	if err := packages.DeletePackageVersion(t.Context(), "acme", "app", 7); err != nil {
		t.Fatal(err)
	}

	want := []string{"GET /orgs/acme/packages/container/app/versions", "DELETE /orgs/acme/packages/container/app/versions/7"}
	if !slices.Equal(requests, want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestPackages_ListsAUserPackageWithASlashInItsName(t *testing.T) {
	t.Parallel()

	srv := &packageServer{deleteStatus: http.StatusNoContent}
	packages := srv.start(t, "/users/alice/packages/container/app%2Fcli/versions",
		"["+versionJSON(1, 'a', "v1.0.0")+","+versionJSON(2, 'b')+"]")

	versions, err := packages.PackageVersions(t.Context(), "alice", "app/cli")
	if err != nil {
		t.Fatal(err)
	}

	if len(versions) != 2 || versions[0].ID != 1 || !slices.Equal(versions[0].Tags, []string{"v1.0.0"}) || len(versions[1].Tags) != 0 {
		t.Fatalf("versions = %+v", versions)
	}
}

func TestPackageTagDeleter(t *testing.T) {
	t.Parallel()

	body := "[" + versionJSON(1, 'a', "v1.0.0", "candidate-v1.0.0") + "," + versionJSON(2, 'b', "staging-old") + "]"
	path := "/orgs/acme/packages/container/app/versions"

	t.Run("deletes a version the tag alone holds", func(t *testing.T) {
		t.Parallel()

		srv := &packageServer{deleteStatus: http.StatusNoContent}
		if err := (PackageTagDeleter{Packages: srv.start(t, path, body)}).DeleteTag(t.Context(), "ghcr.io/acme/app:staging-old"); err != nil {
			t.Fatal(err)
		}

		if !slices.Equal(srv.deleted, []string{"2"}) {
			t.Fatalf("deleted %v, want version 2", srv.deleted)
		}
	})

	t.Run("keeps a version the promoted tag shares", func(t *testing.T) {
		t.Parallel()

		srv := &packageServer{deleteStatus: http.StatusNoContent}

		err := (PackageTagDeleter{Packages: srv.start(t, path, body)}).DeleteTag(t.Context(), "ghcr.io/acme/app:candidate-v1.0.0")
		if !errors.Is(err, errs.ErrUnsupported) || !strings.Contains(err.Error(), "v1.0.0") || len(srv.deleted) != 0 {
			t.Fatalf("err = %v, deleted %v; want the release version kept", err, srv.deleted)
		}
	})

	t.Run("an absent tag is already gone", func(t *testing.T) {
		t.Parallel()

		srv := &packageServer{deleteStatus: http.StatusNoContent}
		if err := (PackageTagDeleter{Packages: srv.start(t, path, body)}).DeleteTag(t.Context(), "ghcr.io/acme/app:nothing"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a token without delete:packages keeps the tag", func(t *testing.T) {
		t.Parallel()

		srv := &packageServer{deleteStatus: http.StatusForbidden}

		err := (PackageTagDeleter{Packages: srv.start(t, path, body)}).DeleteTag(t.Context(), "ghcr.io/acme/app:staging-old")
		if !errors.Is(err, errs.ErrUnsupported) || !strings.Contains(err.Error(), "delete:packages") {
			t.Fatalf("err = %v, want the tag kept with the scope named", err)
		}
	})

	t.Run("refuses another registry", func(t *testing.T) {
		t.Parallel()

		srv := &packageServer{deleteStatus: http.StatusNoContent}
		if err := (PackageTagDeleter{Packages: srv.start(t, path, body)}).DeleteTag(t.Context(), "registry.example/acme/app:x"); !errors.Is(err, errs.ErrUsage) {
			t.Fatalf("err = %v, want a usage refusal", err)
		}
	})
}
