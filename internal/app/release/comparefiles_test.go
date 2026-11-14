// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseFileComparisonIsProducerNeutralAndRejectsInventoryDrift(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"valid", "changed", "extra", "link", "overlap"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			first, second := t.TempDir(), t.TempDir()
			for _, root := range []string{first, second} {
				require.NoError(t, os.WriteFile(filepath.Join(root, "app.jar"), []byte("JVM product"), 0o600))
			}

			expected := []string{"app.jar"}

			switch scenario {
			case "changed":
				require.NoError(t, os.WriteFile(filepath.Join(second, "app.jar"), []byte("changed"), 0o600))
			case "extra":
				require.NoError(t, os.WriteFile(filepath.Join(second, "extra.jar"), []byte("extra"), 0o600))
			case "link":
				require.NoError(t, os.Remove(filepath.Join(second, "app.jar")))
				require.NoError(t, os.Symlink(filepath.Join(first, "app.jar"), filepath.Join(second, "app.jar")))
			case "overlap":
				expected = []string{"app.jar", "app.jar"}

				for _, root := range []string{first, second} {
					require.NoError(t, os.WriteFile(filepath.Join(root, "extra.jar"), []byte("extra"), 0o600))
				}
			}

			err := apprelease.CompareReleaseFiles(first, second, expected, []string{"*.jar"})
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
