// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// ownerRegistryForge is a forge that keeps Maven packages under the owner:
// it names the registry and records, in order, what reaches the registry
// and what it is asked to link.
type ownerRegistryForge struct {
	registry provider.ForgeMavenRegistry

	mu     sync.Mutex
	events []string
}

func (f *ownerRegistryForge) ResolveForgeMavenRegistry() (provider.ForgeMavenRegistry, error) {
	return f.registry, nil
}

func (f *ownerRegistryForge) LinkForgePackage(_ context.Context, packageType, name string) error {
	f.record("link " + packageType + " " + name)

	return nil
}

func (f *ownerRegistryForge) record(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.events = append(f.events, event)
}

// The uploaded publications are linked to the repository once all their
// files are in the registry, and nothing is linked when the upload fails.
func TestUploadForgeMaven_LinksThePackagesAfterTheirUpload(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		status     int
		wantError  error
		wantEvents []string
	}{
		"uploaded": {http.StatusCreated, nil, []string{"upload", "upload", "upload", "upload", "link maven org.example:demo"}},
		"refused":  {http.StatusForbidden, errs.ErrPermissionDenied, []string{"upload"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			directory := t.TempDir()
			version := filepath.Join(directory, "org", "example", "demo", "1.2.3")
			require.NoError(t, os.MkdirAll(version, 0o700))

			pom := `<project><modelVersion>4.0.0</modelVersion><groupId>org.example</groupId><artifactId>demo</artifactId><version>1.2.3</version></project>`
			require.NoError(t, os.WriteFile(filepath.Join(version, "demo-1.2.3.pom"), []byte(pom), 0o600))

			for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar"} {
				require.NoError(t, os.WriteFile(filepath.Join(version, "demo-1.2.3"+suffix), []byte("fixture"), 0o600))
			}

			forge := &ownerRegistryForge{}

			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				forge.record("upload")

				response.WriteHeader(tc.status)
			}))
			t.Cleanup(server.Close)

			forge.registry = provider.ForgeMavenRegistry{ServerID: "forgejo", URL: server.URL, AuthScheme: provider.MavenAuthTokenHeader, Token: "synthetic"}

			err := uploadForgeMaven(t.Context(), forge, directory, "1.2.3", false)
			require.ErrorIs(t, err, tc.wantError)
			require.Equal(t, tc.wantEvents, forge.events)
		})
	}
}
