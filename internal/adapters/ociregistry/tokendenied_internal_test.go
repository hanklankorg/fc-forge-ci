// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// GHCR refuses an anonymous token for a package that does not exist yet, or is
// private, with 403 DENIED at its token endpoint rather than a 404 from the
// manifest. That must read as a refused credential, which a caller can treat
// as a miss, never as an unavailable registry worth retrying.
func TestResolveDigest_ATokenDeniedIsPermissionDenied(t *testing.T) {
	t.Parallel()

	var server *httptest.Server

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}`))

			return
		}

		w.Header().Set("Www-Authenticate", `Bearer realm="`+server.URL+`/token",service="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "http://")

	_, err := WithAuthFile("").ResolveDigest(t.Context(), host+"/acme/app-base:tag")
	if !errors.Is(err, errs.ErrPermissionDenied) {
		t.Fatalf("err = %v, want permission denied", err)
	}
}

// A caller that must reach the registry owner's API reuses the login the
// auth file holds; a registry without a login there is refused, not sent an
// empty token.
func TestAdapterPasswordReadsTheAuthFileLogin(t *testing.T) {
	t.Parallel()

	authFile := filepath.Join(t.TempDir(), "auth.json")
	login := base64.StdEncoding.EncodeToString([]byte("someone:the-token"))

	if err := os.WriteFile(authFile, []byte(`{"auths":{"ghcr.io":{"auth":"`+login+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	adapter := WithAuthFile(authFile)

	if got, err := adapter.Password("ghcr.io"); err != nil || got != "the-token" {
		t.Fatalf("Password(ghcr.io) = %q, %v", got, err)
	}

	if _, err := adapter.Password("registry.example"); !errors.Is(err, errs.ErrMissingInput) {
		t.Fatalf("Password(registry.example) err = %v, want a missing login", err)
	}
}
