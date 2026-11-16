// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
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
