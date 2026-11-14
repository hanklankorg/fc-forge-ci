// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestGitHubExactNestedPackageCleanup(t *testing.T) {
	for _, scenario := range []string{"owned", "nil-owner", "foreign-owner", "id-drift", "owner-drift", "name-drift", "unacknowledged"} {
		t.Run(scenario, func(t *testing.T) {
			const name = "scratch-engine/forge-ci-runtime-base"

			reads, deletes := 0, 0

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if !assert.Equal(t, "/orgs/example-org/packages/container/scratch-engine%2Fforge-ci-runtime-base", request.URL.EscapedPath()) ||
					!assert.Equal(t, "/orgs/example-org/packages/container/scratch-engine%2Fforge-ci-runtime-base", request.URL.RawPath) {
					return
				}

				if request.Method == http.MethodDelete {
					deletes++

					writer.WriteHeader(http.StatusNoContent)

					return
				}

				if !assert.Equal(t, http.MethodGet, request.Method) {
					return
				}

				reads++

				if deletes > 0 {
					writer.WriteHeader(http.StatusNotFound)

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

				assert.NoError(t, json.NewEncoder(writer).Encode(pkg))
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

// The registry names a Maven package after coordinates the harness does not
// know, so the repository binding selects it: the readback and the cleanup
// touch the trial repository's package and leave another repository's alone.
func TestGitHubMavenPackagesAreReadAndRemovedByRepositoryBinding(t *testing.T) {
	const mine, foreign = "org.example.library", "org.example.other"

	var (
		deleted, versionReads []string
		lists                 int
		lag                   bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		name := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/orgs/example-org/packages/maven/"), "/versions")

		switch {
		case request.URL.Path == "/orgs/example-org/packages":
			assert.Equal(t, "maven", request.URL.Query().Get("package_type"))

			if lists++; lag && lists > 1 {
				assert.NoError(t, json.NewEncoder(writer).Encode([]any{}))

				return
			}

			assert.NoError(t, json.NewEncoder(writer).Encode([]map[string]any{
				{"id": 7, "name": mine, "repository": map[string]int64{"id": 42}},
				{"id": 8, "name": foreign, "repository": map[string]int64{"id": 99}},
			}))
		case strings.HasSuffix(request.URL.Path, "/versions"):
			versionReads = append(versionReads, name)

			assert.NoError(t, json.NewEncoder(writer).Encode([]map[string]string{{"name": "1.2.3"}}))
		case request.Method == http.MethodDelete:
			deleted = append(deleted, name)

			writer.WriteHeader(http.StatusNoContent)
		case slices.Contains(deleted, name):
			writer.WriteHeader(http.StatusNotFound)
		default:
			assert.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"id": 7, "name": name, "repository": map[string]int64{"id": 42}}))
		}
	}))
	defer server.Close()

	api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
	require.NoError(t, err)

	t.Run("trial", func(t *testing.T) {
		trial := &GitHubTrial{tb: t, api: api, org: "example-org"}
		trial.removeMavenPackagesAfter("consumer", 42)
		trial.checkReleasedMavenPackages(t.Context(), "consumer", 42, "v1.2.3", "1.2.3")
	})

	require.Equal(t, []string{mine}, versionReads)
	require.Equal(t, []string{mine}, deleted)

	// A listing that has lost the package after the readback must not pass
	// for a cleanup, and no repository means no authority to list at all.
	lag, lists, deleted = true, 0, nil
	recorded := &cleanupErrors{TB: t}
	trial := &GitHubTrial{tb: recorded, api: api, org: "example-org"}
	trial.removeMavenPackagesAfter("consumer", 42)
	trial.checkReleasedMavenPackages(t.Context(), "consumer", 42, "v1.2.3", "1.2.3")
	require.Empty(t, recorded.errors)
	recorded.cleanup()
	require.Len(t, recorded.errors, 1)
	require.Contains(t, recorded.errors[0], "the cleanup removed none")
	require.Empty(t, deleted)

	_, err = trial.mavenPackagesOf(t.Context(), 0)
	require.ErrorIs(t, err, errPackageIdentityInput)
	_, err = trial.readPackageIdentity(t.Context(), packageTypeMaven, GitHubPackageIdentity{Organization: "example-org", Name: mine})
	require.ErrorIs(t, err, errPackageIdentityInput)
}

// cleanupErrors keeps a trial's cleanup and its errors for the test to run
// and read.
type cleanupErrors struct {
	TB

	cleanup func()
	errors  []string
}

func (tb *cleanupErrors) Cleanup(callback func()) { tb.cleanup = callback }
func (tb *cleanupErrors) Errorf(format string, args ...any) {
	tb.errors = append(tb.errors, fmt.Sprintf(format, args...))
}

// The visibilities of a cell run side by side unless its release publishes a
// package the organization names apart from the repository. Every release
// and lifecycle cell is held to the demos known to publish one.
func TestGitHubReleaseSharesPackagesOfTheCells(t *testing.T) {
	shared := []string{"hello-android-library", "hello-monorepo", "hello-npm-package", "hello-npm-service"}

	for _, cell := range GitHubCells {
		if cell.Event != TrialRelease && cell.Event != TrialLifecycle {
			continue
		}

		t.Run(strings.Join(append([]string{cell.Demo, cell.Event}, cell.Settings...), " "), func(t *testing.T) {
			settings, err := ParseGitHubTrialSettings(cell.Event, cell.Settings)
			require.NoError(t, err)

			fixtures := settings.Fixtures
			if fixtures == "" {
				fixtures = "test/live/github"
			}

			got, err := GitHubReleaseSharesPackages(demoConsumerDir(reporoot.Path(t), cell.Demo), fixtures, settings.Workflow)
			require.NoError(t, err)
			assert.Equal(t, slices.Contains(shared, cell.Demo), got)
		})
	}
}

// A container under a declared image-name is the organization's, whatever
// the repository: both visibilities would publish it.
func TestGitHubReleaseSharesAnImageName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "test", "live", "github", "workflows", "release.yml"),
		"jobs:\n  release:\n    with:\n      artifacts-config: .forge-ci/artifacts.yml\n")
	writeFile(t, filepath.Join(root, ".forge-ci", "artifacts.yml"),
		"artifacts:\n  - name: app\n    project-type: meta\ncontainers:\n  - name: app\n    from: [app]\n    container-file: Containerfile\n    image-name: example-org/shared-image\n")

	shared, err := GitHubReleaseSharesPackages(root, "test/live/github", "release.yml")
	require.NoError(t, err)
	assert.True(t, shared)
}
