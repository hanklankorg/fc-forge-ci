// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestGatherExportsPreflightsAllTreesAndPreservesHiddenExecutableFiles(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"valid", "duplicate", "link", "missing", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			first, second := t.TempDir(), t.TempDir()
			dest := filepath.Join(t.TempDir(), "dist")
			require.NoError(t, os.WriteFile(filepath.Join(first, "..hidden"), []byte("first"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(second, "other"), []byte("second"), 0o600))

			switch scenario {
			case "duplicate":
				require.NoError(t, os.WriteFile(filepath.Join(second, "..hidden"), nil, 0o600))
			case "link":
				require.NoError(t, os.Symlink("/etc", filepath.Join(second, "escape")))
			case "missing":
				second = filepath.Join(second, "absent")
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			if scenario == "cancelled" {
				cancel()
			}

			err := apprelease.GatherExports(ctx, apprelease.GatherExportsInput{Sources: []string{first, second}, Destination: dest, Enabled: true})
			if scenario != "valid" {
				require.Error(t, err)
				require.NoDirExists(t, dest)

				return
			}

			require.NoError(t, err)
			body, err := os.ReadFile(filepath.Join(dest, "..hidden"))
			require.NoError(t, err)
			require.Equal(t, "first", string(body))

			info, err := os.Stat(filepath.Join(dest, "..hidden"))
			require.NoError(t, err)
			require.NotZero(t, info.Mode().Perm()&0o111)
			require.ErrorContains(t, apprelease.GatherExports(ctx, apprelease.GatherExportsInput{Sources: []string{first}, Destination: dest, Enabled: true}), "must not exist yet")
		})
	}
}

func TestGatherExportsNamesAMissingSource(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "dist")
	err := apprelease.GatherExports(t.Context(), apprelease.GatherExportsInput{Sources: []string{missing}, Destination: t.TempDir(), Enabled: true})
	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, "export commands must write it")
}
