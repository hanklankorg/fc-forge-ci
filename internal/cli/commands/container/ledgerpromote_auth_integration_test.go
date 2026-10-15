//go:build integration

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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestLedgerPromote_AuthFileOverridesAmbientCredentials(t *testing.T) {
	testenv.New(t)

	cosignBinary := evidence.RequireTool(t, "cosign")
	store := registry.New(registry.Logger(log.New(io.Discard, "", 0)))

	var rejected atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, _ := r.BasicAuth()
		if user != "selected" || password != "fixture" {
			if user != "" {
				rejected.Add(1)
			}

			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		store.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "http://")
	source := host + "/source/app"
	image, err := random.Image(32, 1)
	require.NoError(t, err)

	digest, err := image.Digest()
	require.NoError(t, err)

	opts := []crane.Option{crane.WithContext(t.Context()), crane.Insecure, crane.WithAuth(&authn.Basic{Username: "selected", Password: "fixture"})}
	require.NoError(t, crane.Push(image, source+":staging-v1.2.3", opts...))

	config := func(user string) []byte {
		return []byte(fmt.Sprintf(`{"auths":{%q:{"username":%q,"password":"fixture"}}}`, host, user))
	}
	ambient := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ambient, "config.json"), config("ambient"), 0o600))
	t.Setenv("DOCKER_CONFIG", ambient)

	authFile := filepath.Join(t.TempDir(), "selected.json")
	require.NoError(t, os.WriteFile(authFile, config("selected"), 0o600))
	t.Setenv("REGISTRY_AUTH_FILE", authFile)

	// Positive precedence check against the real tool: the ambient Docker
	// config wins over REGISTRY_AUTH_FILE, so forwarding that variable is not enough.
	adapter := &cosign.Adapter{Bin: cosignBinary}
	require.Error(t, adapter.CopyImage(t.Context(), cosign.CopyImageInput{Source: source + "@" + digest.String(), Dest: host + "/control:dev"}, io.Discard))
	require.Positive(t, rejected.Load())
	rejected.Store(0)

	ledger, err := imageledger.Marshal([]imageledger.Entry{{
		Ref: source + "@" + digest.String(), Digest: digest.String(),
		FinalTag: source + ":v1.2.3",
	}})
	require.NoError(t, err)

	ledgerPath := filepath.Join(t.TempDir(), "ledger.json")
	require.NoError(t, os.WriteFile(ledgerPath, ledger, 0o600))

	cmd := ledgerPromoteCmd()
	require.NoError(t, cmd.Run(t.Context(), []string{cmd.Name, "--ledger", ledgerPath, "--stage", "dev", "--stage-repo", host + "/target", "--auth-file", authFile, "--allow-digest-ref-fallback"}))

	got, err := crane.Digest(host+"/target/source/app:dev", opts...)
	require.NoError(t, err)
	require.Equal(t, digest.String(), got)
	require.Zero(t, rejected.Load(), "explicit auth must reach the signature copier too")
	require.Equal(t, ambient, os.Getenv("DOCKER_CONFIG"), "the command must not alter the caller's auth configuration")
}
