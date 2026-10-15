// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/stretchr/testify/require"
)

// Exercise the actual workflow shell, CLI flag/env wiring, artifact records and
// registry adapter together. Only the owned TLS registry receives requests.
func TestLayoutPublicationWorkflowUsesExplicitAuthAndChecksEveryArtifact(t *testing.T) {
	t.Parallel()
	engine := forgeCIEngine(t)

	selected := jobStep(t, ".github/workflows/publish-container.yml", "publish-images", "Verify all layouts and publish their exact digests")
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("broken-sibling=%t", broken), func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "layout-publication")

			var requests, wrongCredentials atomic.Int32

			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)

				user, password, ok := r.BasicAuth()
				if !ok || user != "fixture-publisher" || password != "fixture-password" {
					if ok {
						wrongCredentials.Add(1)
					}

					w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
					w.WriteHeader(http.StatusUnauthorized)

					return
				}

				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			host := strings.TrimPrefix(server.URL, "https://")
			repository := host + "/org/image"
			source := strings.Repeat("b", 40)
			certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			caFile := box.write("ca.pem", string(certificate), 0o600)
			authFile := box.write("explicit/auth.json", fmt.Sprintf(`{"auths":{%q:{"username":"fixture-publisher","password":"fixture-password"}}}`, host), 0o600)
			box.write("ambient/config.json", fmt.Sprintf(`{"auths":{%q:{"username":"wrong","password":"wrong"}}}`, host), 0o600)

			values := map[string]string{
				"${{ needs.prep.outputs.image-name }}":                          repository,
				"${{ needs.prep.outputs.source-sha }}":                          source,
				"${{ join(fromJson(needs.prep.outputs.platforms-json), ',') }}": "linux/amd64,linux/arm64",
				"${{ steps.registry-login.outputs.auth-file }}":                 authFile,
			}
			env := make([]string, 0, 12+len(selected.Env))
			env = append(env, "FORGE_CI_BIN="+engine, "FORGE_CI_PROVIDER=github", "FORGE_CI_RUNNER=github", "GITHUB_OUTPUT="+box.outputs,
				"RUNNER_TEMP="+box.root, "SSL_CERT_FILE="+caFile, "SSL_CERT_DIR="+filepath.Join(box.root, "empty-ca-dir"),
				"DOCKER_CONFIG="+filepath.Join(box.root, "ambient"))

			for key, expression := range selected.Env {
				value, ok := values[expression]
				require.True(t, ok, "unhandled workflow expression %s", expression)

				env = append(env, key+"="+value)
			}

			want := map[string]string{}

			for _, arch := range []string{"amd64", "arm64"} {
				image, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: arch, Config: v1.Config{Labels: map[string]string{"org.opencontainers.image.revision": source}}})
				require.NoError(t, err)

				directory := filepath.Join(box.root, "image-layouts", arch)
				root, err := layout.Write(directory, empty.Index)
				require.NoError(t, err)
				require.NoError(t, root.AppendImage(image))
				digest, err := image.Digest()
				require.NoError(t, err)

				want["linux/"+arch] = digest.String()
				got := box.run(`"$FORGE_CI_BIN" container layout record --layout-dir "$LAYOUT_DIR" --platform "$PLATFORM"`, append(env, "LAYOUT_DIR="+directory, "PLATFORM=linux/"+arch)...)
				require.Zero(t, got.status, "%s", got.stderr)
				require.Equal(t, "digest="+digest.String()+"\n", box.output())
			}

			require.Zero(t, requests.Load(), "recording must remain offline")

			if broken {
				require.NoError(t, os.Remove(filepath.Join(box.root, "image-layouts", "arm64", "index.json")))
			}
			// Bound a failing authentication regression to one attempt.
			script := "forge-ci() { \"$FORGE_CI_BIN\" \"$@\"; }\n" + strings.TrimSpace(selected.Run) + " --retry-attempts 1\n"

			got := box.run(script, env...)
			if broken {
				require.NotZero(t, got.status)
				require.Empty(t, box.output())
				require.Zero(t, requests.Load())

				return
			}

			require.Zero(t, got.status, "%s", got.stderr)

			var digests map[string]string
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(box.output(), "platform-digests="))), &digests))
			require.Equal(t, want, digests)
			require.Positive(t, requests.Load())
			require.Zero(t, wrongCredentials.Load(), "the explicit file must override ambient credentials")
		})
	}
}
