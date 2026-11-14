// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// demoOrganization is a GitHub organization with one demo, repository 42, and
// packages of each type: bound to the demo, bound to another repository, and
// bound to none.
type demoOrganization struct {
	t *testing.T
	// packages maps "<type> <name>" to the ID of the repository the package
	// is bound to, 0 for none. A deleted package is removed.
	packages map[string]int64
	// keeps names a package the registry reports as still there after its
	// deletion; rebinds one that moves to repository 99 after the listing.
	keeps, rebinds string
	listed         bool
	log            []string
}

func (org *demoOrganization) handle(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.EscapedPath()

	switch {
	case path == "/orgs/example-org/repos":
		assert.NoError(org.t, json.NewEncoder(writer).Encode([]map[string]any{
			{"id": 42, "name": "fc-demo-hello", "html_url": "https://github.example/example-org/fc-demo-hello"},
			{"id": 99, "name": "fc-other"},
		}))
	case path == "/orgs/example-org/packages":
		org.listed = true

		page := []map[string]any{}

		for key, repository := range org.packages {
			packageType, name, _ := strings.Cut(key, " ")
			if packageType == request.URL.Query().Get("package_type") {
				page = append(page, org.pkg(name, repository))
			}
		}

		assert.NoError(org.t, json.NewEncoder(writer).Encode(page))
	case strings.HasPrefix(path, "/orgs/example-org/packages/"):
		packageType, escaped, _ := strings.Cut(strings.TrimPrefix(path, "/orgs/example-org/packages/"), "/")
		key := packageType + " " + strings.ReplaceAll(escaped, "%2F", "/")

		repository, found := org.packages[key]
		if !found {
			writer.WriteHeader(http.StatusNotFound)

			return
		}

		if request.Method == http.MethodDelete {
			org.log = append(org.log, "delete "+key)

			if key != org.keeps {
				delete(org.packages, key)
			}

			writer.WriteHeader(http.StatusNoContent)

			return
		}

		if key == org.rebinds && org.listed {
			repository = 99
		}

		assert.NoError(org.t, json.NewEncoder(writer).Encode(org.pkg(strings.TrimPrefix(key, packageType+" "), repository)))
	default:
		org.t.Errorf("unexpected request %s %s", request.Method, path)
	}
}

func (org *demoOrganization) pkg(name string, repository int64) map[string]any {
	pkg := map[string]any{"id": int64(len(name)), "name": name}
	if repository > 0 {
		pkg["repository"] = map[string]int64{"id": repository}
	}

	return pkg
}

func (org *demoOrganization) demos() *GitHubDemos {
	server := httptest.NewServer(http.HandlerFunc(org.handle))
	org.t.Cleanup(server.Close)

	api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
	require.NoError(org.t, err)

	return &GitHubDemos{
		api: api, org: "example-org", prefix: "fc-",
		deleteRepository: func(_ context.Context, name string, id int64) error {
			org.log = append(org.log, "delete repository "+name)
			assert.Equal(org.t, int64(42), id)

			return nil
		},
	}
}

func demoPackages() map[string]int64 {
	return map[string]int64{
		"container fc-demo-hello/hello": 42,
		"npm fc-demo-hello":             42,
		"maven se.example.hello":        42,
		"container fc-other/tool":       99,
		"npm fc-demo-hello-orphan":      0,
		"container fc-demo-hello":       0,
	}
}

// A demo's repository goes with the packages GitHub binds to it, the packages
// first. A package of another repository, and one bound to none, stay: a
// name alone does not make a package the demo's.
func TestGitHubDemoIsDeletedWithThePackagesBoundToIt(t *testing.T) {
	org := &demoOrganization{t: t, packages: demoPackages()}
	demos := org.demos()

	sets, err := demos.List(t.Context())
	require.NoError(t, err)
	require.Len(t, sets, 1)
	require.Equal(t, "hello", sets[0].Name)
	require.Len(t, sets[0].Repositories, 1)
	require.ElementsMatch(t, []DemoPackage{
		{Type: "container", Name: "fc-demo-hello/hello", ID: int64(len("fc-demo-hello/hello"))},
		{Type: "npm", Name: "fc-demo-hello", ID: int64(len("fc-demo-hello"))},
		{Type: "maven", Name: "se.example.hello", ID: int64(len("se.example.hello"))},
	}, sets[0].Repositories[0].Packages)

	require.NoError(t, demos.Delete(t.Context(), sets[0]))

	require.Len(t, org.log, 4)
	require.ElementsMatch(t, []string{"delete container fc-demo-hello/hello", "delete npm fc-demo-hello", "delete maven se.example.hello"}, org.log[:3])
	require.Equal(t, "delete repository fc-demo-hello", org.log[3])
	require.Equal(t, map[string]int64{"container fc-other/tool": 99, "npm fc-demo-hello-orphan": 0, "container fc-demo-hello": 0}, org.packages)
}

// A package that cannot be shown gone, or that is no longer the listed
// package of the listed repository, stops the deletion with the repository
// still there: deleting it would unbind what is left.
func TestGitHubDemoRepositoryStaysWhenAPackageDoes(t *testing.T) {
	for name, org := range map[string]*demoOrganization{
		"deletion not confirmed": {keeps: "npm fc-demo-hello"},
		"bound elsewhere since":  {rebinds: "npm fc-demo-hello"},
	} {
		t.Run(name, func(t *testing.T) {
			org.t, org.packages = t, map[string]int64{"npm fc-demo-hello": 42}
			demos := org.demos()

			sets, err := demos.List(t.Context())
			require.NoError(t, err)
			require.Len(t, sets, 1)

			err = demos.Delete(t.Context(), sets[0])
			if org.keeps != "" {
				require.ErrorIs(t, err, errPackageDeletionUnconfirmed)
			} else {
				require.ErrorIs(t, err, errPackageIdentityDrift)
				require.Empty(t, org.log)
			}

			require.NotContains(t, org.log, "delete repository fc-demo-hello")
			require.Contains(t, org.packages, "npm fc-demo-hello")
		})
	}
}
