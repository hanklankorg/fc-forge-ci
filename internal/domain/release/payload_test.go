// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"io/fs"
	"testing"
	"testing/fstest"

	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/stretchr/testify/require"
)

func TestCanonicalPayloadAdmission(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"dist/asset", "premade-sboms/base.cdx.json", "other/asset", "digest", "dist/release-images-publication.json", "premade-sboms/base-images-publication.json", "dist/back\\slash", "dist/control\n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := fstest.MapFS{"dist/asset": {Data: []byte("asset")}, name: {Data: []byte("{}")}}

			err := domainrelease.AdmitCanonicalPayload(tree)
			if name == "dist/asset" || name == "premade-sboms/base.cdx.json" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}

	for _, mode := range []fs.FileMode{fs.ModeSymlink, fs.ModeNamedPipe, fs.ModeSocket, fs.ModeDevice} {
		require.Error(t, domainrelease.AdmitCanonicalPayload(fstest.MapFS{"dist/asset": {Mode: mode}}))
	}

	require.Error(t, domainrelease.AdmitCanonicalPayload(fstest.MapFS{"premade-sboms/base": {Data: []byte("{}")}}))
	require.Error(t, domainrelease.AdmitCanonicalPayload(fstest.MapFS{"dist": {Data: []byte("collision")}}))
}
