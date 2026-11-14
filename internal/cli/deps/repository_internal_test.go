// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package deps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/forgejo"
	"github.com/diggsweden/forge-ci/internal/adapters/github"
	"github.com/diggsweden/forge-ci/internal/adapters/gitlab"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/repository"
	"github.com/diggsweden/forge-ci/internal/testutil/repositorycases"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestRepositoryParsersPreserveSourceIdentityWithoutEnvironment(t *testing.T) {
	t.Parallel()

	for _, forge := range provider.AllForges() {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			adapter, err := providerForWithEnv(forge, func(key string) string {
				t.Fatalf("pure repository parsing read environment %q", key)

				return ""
			})
			if err != nil {
				t.Fatal(err)
			}

			parser, err := RoleFrom[repository.Parser](adapter, "repository addressing")
			if err != nil {
				t.Fatal(err)
			}

			for _, tc := range repositorycases.OwnerName() {
				path, err := parser.ParseRepository(tc.Path)
				if (err == nil) != tc.Valid {
					t.Errorf("%q: err=%v, want valid=%v", tc.Path, err, tc.Valid)
				}

				if tc.Valid {
					namespace, name, _ := strings.Cut(tc.Path, "/")
					if path.String() != tc.Path || path.Namespace() != namespace || path.Name() != name {
						t.Errorf("source spelling changed: %q -> %#v", tc.Path, path)
					}
				}
			}
		})
	}
}

func TestRepositoryParserSelectionUsesForgeNotRunner(t *testing.T) {
	for _, forge := range provider.AllForges() {
		t.Run(string(forge), func(t *testing.T) {
			testenv.New(t)
			t.Setenv("FORGE_CI_PROVIDER", string(forge))
			t.Setenv("FORGE_CI_RUNNER", "github")

			path, err := ParseRepository("Group/Sub-Group/My_Repo.v2")

			wantNested := forge == provider.ForgeGitLab || forge == provider.ForgeLocal
			if (err == nil) != wantNested {
				t.Fatalf("nested path: err=%v, want accepted=%v", err, wantNested)
			}

			if wantNested && (path.Namespace() != "Group/Sub-Group" || path.Name() != "My_Repo.v2") {
				t.Fatalf("nested source identity lost: %#v", path)
			}
		})
	}
}

func TestRepositoryAPIsRetainCaseAndEscapeOnlyAtTransportBoundary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, path, requestURI string
		newProvider            func(string) provider.RepoMetadataFetcher
	}{
		{"github", "MixedOrg/My.Repo", "/repos/MixedOrg/My.Repo", func(base string) provider.RepoMetadataFetcher {
			return &github.Provider{APIBaseOverride: base, Env: func(string) string { return "" }}
		}},
		{"forgejo", "MixedOrg/.github", "/api/v1/repos/MixedOrg/.github", func(base string) provider.RepoMetadataFetcher {
			return &forgejo.Provider{APIBaseOverride: base, Env: func(string) string { return "" }}
		}},
		{"gitlab", "Group/Sub-Group/My_Repo.v2", "/api/v4/projects/Group%2FSub-Group%2FMy_Repo.v2", func(base string) provider.RepoMetadataFetcher {
			return &gitlab.Provider{APIBaseOverride: base, Env: func(string) string { return "" }}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			requests := make(chan string, 1)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests <- request.RequestURI

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"description":"source"}`))
			}))
			defer server.Close()

			metadata, err := tc.newProvider(server.URL).FetchRepoMetadata(t.Context(), tc.path)
			if err != nil {
				t.Fatal(err)
			}

			if metadata.Description != "source" {
				t.Fatalf("metadata=%+v", metadata)
			}

			select {
			case got := <-requests:
				if got != tc.requestURI {
					t.Fatalf("request URI=%q, want %q", got, tc.requestURI)
				}
			default:
				t.Fatal("no repository request")
			}
		})
	}
}
