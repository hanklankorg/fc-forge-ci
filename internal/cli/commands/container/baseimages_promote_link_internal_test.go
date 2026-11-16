// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// basePackageLinker is a forge that keeps packages under their owner and
// records what it is asked to link.
type basePackageLinker struct{ linked []string }

func (f *basePackageLinker) LinkForgePackage(_ context.Context, packageType, name string) error {
	f.linked = append(f.linked, packageType+" "+name)

	return nil
}

// nonLinkingForge keeps packages in the repository already, as GitHub and
// GitLab do, and so links none.
type nonLinkingForge struct{}

// Promoted base images on the forge's own registry are one package of the
// owner's, named by its path beneath the owner, and it is linked once to the
// repository. A forge that does not link packages, and a base repository on
// another registry, link nothing.
func TestLinkBaseImagePackages_LinksTheBasePackageOnAForgeThatKeepsItUnderTheOwner(t *testing.T) {
	t.Parallel()

	images := []appbaseimages.BaseImageMetadata{
		{Flavor: "core", Tag: "forge.example/owner/app-base:core-1"},
		{Flavor: "tools", Tag: "forge.example/owner/app-base:tools-1"},
	}
	own := baseImagesCommon{Registry: "forge.example", Repository: "owner/app"}

	for _, tc := range []struct {
		name   string
		common baseImagesCommon
		images []appbaseimages.BaseImageMetadata
		want   []string
	}{
		{name: "forge's own registry", common: own, images: images, want: []string{"container app-base"}},
		{
			name:   "another registry",
			common: baseImagesCommon{Registry: "ghcr.io", Repository: "owner/app", Foreign: true},
			images: []appbaseimages.BaseImageMetadata{{Flavor: "core", Tag: "ghcr.io/owner/app-base:core-1"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			forge := &basePackageLinker{}

			var stderr bytes.Buffer

			linkBaseImagePackages(t.Context(), forge, &stderr, tc.common, tc.images)
			require.Equal(t, tc.want, forge.linked)
			require.Empty(t, stderr.String())
		})
	}

	t.Run("forge without package links", func(t *testing.T) {
		t.Parallel()

		var stderr bytes.Buffer

		linkBaseImagePackages(t.Context(), nonLinkingForge{}, &stderr, own, images)
		require.Empty(t, stderr.String())
	})
}

// On Forgejo the link carries the package-API token, else the forge's own
// token, and never the cross-forge $CI_TOKEN or GitHub's $GITHUB_TOKEN,
// which the adapter would otherwise read ahead of or after them. With none
// of the first two, no request is made and the link is a warning.
func TestBaseImagesPackageForge_LinksWithTheForgesOwnTokenOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string // each set to "<name>-value"
		want string   // empty: no request, a warning
	}{
		{name: "package-API token", env: []string{"CI_TOKEN", packageAPITokenEnv, "FORGEJO_TOKEN"}, want: packageAPITokenEnv + "-value"},
		{name: "forge's own token", env: []string{"CI_TOKEN", "FORGEJO_TOKEN", "GITHUB_TOKEN"}, want: "FORGEJO_TOKEN-value"},
		{name: "another forge's token only", env: []string{"CI_TOKEN", "GITHUB_TOKEN"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testenv.New(t)
			t.Setenv("FORGE_CI_PROVIDER", "forgejo")

			for _, variable := range tc.env {
				t.Setenv(variable, variable+"-value")
			}

			var (
				mu    sync.Mutex
				auths []string
			)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()

				auths = append(auths, r.Header.Get("Authorization"))

				mu.Unlock()

				if r.URL.Path == "/api/v1/version" {
					_, _ = w.Write([]byte(`{"version":"11.0.0"}`))

					return
				}

				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)

			serverURL, err := url.Parse(server.URL)
			require.NoError(t, err)

			common := baseImagesCommon{Registry: serverURL.Host, Repository: "owner/app", ServerURL: server.URL}

			forge, err := baseImagesPackageForge(common)
			require.NoError(t, err)

			var stderr bytes.Buffer

			linkBaseImagePackages(t.Context(), forge, &stderr, common,
				[]appbaseimages.BaseImageMetadata{{Flavor: "core", Tag: serverURL.Host + "/owner/app-base:core-1"}})

			mu.Lock()
			defer mu.Unlock()

			if tc.want == "" {
				require.Empty(t, auths)
				require.Contains(t, stderr.String(), "not linked to the repository")
				require.Contains(t, stderr.String(), "$"+packageAPITokenEnv)

				return
			}

			require.NotEmpty(t, auths)

			for _, auth := range auths {
				require.Equal(t, "token "+tc.want, auth)
			}
		})
	}
}
