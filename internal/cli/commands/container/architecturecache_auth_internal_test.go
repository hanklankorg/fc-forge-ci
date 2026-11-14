// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestStageArchitectureCacheUsesExplicitAuthWithoutAmbientFallback(t *testing.T) {
	testenv.New(t)
	store := registry.New(registry.Logger(log.New(io.Discard, "", 0)))

	var authenticated atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, _ := r.BasicAuth()
		if user != "selected" || password != "fixture" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		authenticated.Add(1)
		store.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	repository := host + "/owner/base"
	image, err := random.Image(32, 1)
	require.NoError(t, err)
	config, err := image.ConfigFile()
	require.NoError(t, err)

	config.OS = "linux"
	config.Architecture = runtime.GOARCH
	revision := strings.Repeat("a", 40)
	config.Config.Labels = map[string]string{"org.opencontainers.image.revision": revision, "org.example.input": "reviewed"}
	image, err = mutate.ConfigFile(image, config)
	require.NoError(t, err)
	require.NoError(t, crane.Push(image, repository+":cache", crane.WithContext(t.Context()), crane.Insecure, crane.WithAuth(&authn.Basic{Username: "selected", Password: "fixture"})))

	credentials := []byte(fmt.Sprintf(`{"auths":{%q:{"username":"selected","password":"fixture"}}}`, host))
	authFile := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, os.WriteFile(authFile, credentials, 0o600))
	ambient := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ambient, "config.json"), credentials, 0o600))
	t.Setenv("DOCKER_CONFIG", ambient)
	t.Run("private-parent-artifact", func(t *testing.T) {
		digest, err := image.Digest()
		require.NoError(t, err)

		ref := repository + "@" + digest.String()
		directory := filepath.Join(t.TempDir(), "parent")
		fetch := layoutFetchInputCmd()
		require.NoError(t, fetch.Run(t.Context(), []string{fetch.Name,
			"--image-ref", ref, "--platform", "linux/" + runtime.GOARCH,
			"--layout-dir", directory, "--auth-file", authFile}))
		// Verification receives no credentials and must issue no registry calls.
		before := authenticated.Load()
		verify := layoutVerifyInputCmd()
		require.NoError(t, verify.Run(t.Context(), []string{verify.Name,
			"--image-ref", ref, "--platform", "linux/" + runtime.GOARCH,
			"--layout-dir", directory}))
		require.Equal(t, before, authenticated.Load())
	})

	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%t", explicit), func(t *testing.T) {
			authenticated.Store(0)
			t.Chdir(t.TempDir())

			metadata := "metadata"
			cmd := signerImageBuildArchCmd()

			args := []string{cmd.Name, "--stage", "--cache-only", "--arch", runtime.GOARCH, "--source-sha", revision,
				"--server-url", "https://forge.example", "--repository", "owner/app", "--image-repository", repository,
				"--name", "base", "--cache-tag", "cache", "--require-label", "org.example.input=reviewed", "--metadata-dir", metadata}
			if explicit {
				args = append(args, "--auth-file", authFile)
			}

			err := cmd.Run(t.Context(), args)
			if explicit {
				require.NoError(t, err)
				require.Positive(t, authenticated.Load())
				require.FileExists(t, filepath.Join(metadata, "base-"+runtime.GOARCH+".json"))
				// A mismatched cache must refuse without invoking any builder.
				args = append(args, "--require-label", "org.example.absent=required")
				err = cmd.Run(t.Context(), args)
				require.ErrorContains(t, err, "no matching architecture cache")
			} else {
				require.Error(t, err)
				require.Zero(t, authenticated.Load(), "ambient credentials must not authenticate staging")
			}
		})
	}
}
