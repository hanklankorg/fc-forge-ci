// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/require"
)

func TestGitHubExactNestedPackageCleanup(t *testing.T) {
	for _, scenario := range []string{"owned", "nil-owner", "foreign-owner", "id-drift", "owner-drift", "name-drift", "unacknowledged"} {
		t.Run(scenario, func(t *testing.T) {
			const name = "scratch-engine/forge-ci-runtime-base"
			reads, deletes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/orgs/example-org/packages/container/scratch-engine%2Fforge-ci-runtime-base", r.URL.EscapedPath())
				require.Equal(t, "/orgs/example-org/packages/container/scratch-engine%2Fforge-ci-runtime-base", r.URL.RawPath)
				if r.Method == http.MethodDelete {
					deletes++
					w.WriteHeader(http.StatusNoContent)
					return
				}
				require.Equal(t, http.MethodGet, r.Method)
				reads++
				if deletes > 0 {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				pkg := map[string]any{"id": int64(77), "name": name, "repository": map[string]int64{"id": 42}}
				if scenario == "nil-owner" {
					delete(pkg, "repository")
				}
				if scenario == "foreign-owner" || (scenario == "owner-drift" && reads > 1) {
					pkg["repository"] = map[string]int64{"id": 99}
				}
				if scenario == "id-drift" && reads > 1 {
					pkg["id"] = int64(78)
				}
				if scenario == "name-drift" && reads > 1 {
					pkg["name"] = "other"
				}
				require.NoError(t, json.NewEncoder(w).Encode(pkg))
			}))
			defer server.Close()
			api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
			require.NoError(t, err)
			trial := &GitHubTrial{api: api, org: "example-org"}
			identity, err := trial.ReadContainerPackageIdentity(t.Context(), name, 42)
			if scenario == "nil-owner" || scenario == "foreign-owner" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				if scenario == "unacknowledged" {
					identity.ID = 0
				}
				err = trial.DeleteContainerPackage(t.Context(), identity)
				if scenario == "owned" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			}
			if scenario == "owned" {
				require.Equal(t, 1, deletes)
				require.Equal(t, 3, reads)
			} else {
				require.Zero(t, deletes)
			}
		})
	}
}

func TestGitHubRuntimePackageCoordinates(t *testing.T) {
	engine := GitHubEngine{Owner: "Example-Org", Repository: "Scratch-Engine", Namespace: "ghcr.io/example-org/scratch-engine"}
	names, err := engine.RuntimePackageNames()
	require.NoError(t, err)
	require.Equal(t, []string{"scratch-engine/forge-ci-runtime-base", "scratch-engine/forge-ci-runtime", "scratch-engine/forge-ci-runtime-rust-stable", "scratch-engine/forge-ci-runtime-go-1.27", "scratch-engine/forge-ci-runtime-node-24", "scratch-engine/forge-ci-runtime-java-25", "scratch-engine/forge-ci-runtime-android-35", "scratch-engine-buildcache"}, names)
	engine.Namespace = ""
	names, err = engine.RuntimePackageNames()
	require.NoError(t, err)
	require.Equal(t, "forge-ci-runtime-base", names[0])
	engine.Namespace = "ghcr.io/foreign/engine"
	_, err = engine.RuntimePackageNames()
	require.Error(t, err)
}
