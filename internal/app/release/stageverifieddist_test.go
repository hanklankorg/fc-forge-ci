// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/stretchr/testify/require"
)

func TestStageVerifiedDistChecksInstalledBytesAndCleansFailedStaging(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"valid", "tampered", "link", "preseeded", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			source := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(source, ".asset"), []byte("reviewed bytes"), 0o755))
			digest, err := apprelease.DistDigestWithManifestRoot(source, ".")
			require.NoError(t, err)
			destination := filepath.Join(t.TempDir(), "dist")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			switch scenario {
			case "tampered":
				require.NoError(t, os.WriteFile(filepath.Join(source, ".asset"), []byte("tampered bytes"), 0o755))
			case "link":
				require.NoError(t, os.Symlink(".asset", filepath.Join(source, "escape")))
			case "preseeded":
				require.NoError(t, os.Mkdir(destination, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(destination, "keep"), []byte("owned"), 0o600))
			case "cancelled":
				cancel()
			}

			err = apprelease.StageVerifiedDist(ctx, source, destination, digest, ".")
			if scenario == "valid" {
				require.NoError(t, err)
				require.NoError(t, apprelease.VerifyDistWithManifestRoot(destination, digest, "."))
				info, err := os.Stat(filepath.Join(destination, ".asset"))
				require.NoError(t, err)
				require.NotZero(t, info.Mode().Perm()&0o111)

				entries, err := os.ReadDir(destination)
				require.NoError(t, err)
				require.Len(t, entries, 1)
			} else {
				require.Error(t, err)

				if scenario == "preseeded" {
					require.FileExists(t, filepath.Join(destination, "keep"))
				} else {
					require.NoDirExists(t, destination)
				}
			}
		})
	}
}

func TestReadDistDigestFileIsStrictData(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "digest")
	for _, data := range []string{"$(touch exploit)", strings.Repeat("A", 64), " " + strings.Repeat("a", 64), strings.Repeat("a", 64) + " extra"} {
		require.NoError(t, os.WriteFile(file, []byte(data), 0o600))
		_, err := apprelease.ReadDistDigestFile(file)
		require.Error(t, err)
	}

	require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("a", 64)+"\n"), 0o600))
	digest, err := apprelease.ReadDistDigestFile(file)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("a", 64), digest)

	link := file + ".link"
	require.NoError(t, os.Symlink(file, link))
	_, err = apprelease.ReadDistDigestFile(link)
	require.Error(t, err)
}
