// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"os"
	"path/filepath"
	"testing"

	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/stretchr/testify/require"
)

func TestCanonicalInventoryAdmissionPrecedesAnyCopy(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"zz-unexpected/file", "dist/zz-control\n", "premade-sboms/release-images-publication.json"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()

			source, destination := t.TempDir(), t.TempDir()
			for _, name := range []string{"dist/aaa-valid", bad} {
				full := filepath.Join(source, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
				require.NoError(t, os.WriteFile(full, []byte("bytes"), 0o600))
			}

			root, err := pathsafe.OpenRoot(destination)
			require.NoError(t, err)

			defer func() { _ = root.Close() }()

			err = gatherExportTreesAdmitted(t.Context(), GatherExportsInput{Sources: []string{source}, Destination: destination, Enabled: true}, root, domainrelease.AdmitCanonicalPayload)
			require.Error(t, err)
			entries, err := os.ReadDir(destination)
			require.NoError(t, err)
			require.Empty(t, entries, "even the earlier valid file must not be copied")
		})
	}
}
