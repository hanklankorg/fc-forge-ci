// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func splitReleaseImageSources(t *testing.T, fixture *releaseImagesPublishFixture) {
	t.Helper()
	roots := map[string]string{"amd64": t.TempDir(), "arm64": t.TempDir()}
	entries, err := os.ReadDir(fixture.in.LayoutsDir)
	require.NoError(t, err)

	for _, entry := range entries {
		arch := "amd64"
		if strings.HasSuffix(entry.Name(), "-arm64") {
			arch = "arm64"
		}

		require.NoError(t, os.Rename(filepath.Join(fixture.in.LayoutsDir, entry.Name()), filepath.Join(roots[arch], entry.Name())))
	}

	fixture.in.LayoutsDir = ""
	fixture.in.LayoutSources = roots
}

func TestReleaseImagesPublishesAdmittedNativeSourcesWithoutMerging(t *testing.T) {
	fixture := newReleaseImagesPublishFixture(t)
	splitReleaseImageSources(t, &fixture)
	require.NoError(t, fixture.publish(t))
	require.Equal(t, []string{"linux/amd64", "linux/arm64", "linux/amd64"}, *fixture.gated)
	require.FileExists(t, filepath.Join(fixture.in.LayoutSources["arm64"], "app-arch-arm64", "app-arm64.json"))
}

func TestReleaseImagesRefusesNativeSourceDriftBeforeScansOrWrites(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong-arch", "linked-root"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newReleaseImagesPublishFixture(t)
			splitReleaseImageSources(t, &fixture)

			switch scenario {
			case "missing":
				delete(fixture.in.LayoutSources, "arm64")
			case "wrong-arch":
				require.NoError(t, os.Rename(filepath.Join(fixture.in.LayoutSources["arm64"], "app-arch-arm64"), filepath.Join(fixture.in.LayoutSources["amd64"], "app-arch-arm64")))
			case "linked-root":
				link := filepath.Join(t.TempDir(), "linked")
				require.NoError(t, os.Symlink(fixture.in.LayoutSources["arm64"], link))
				fixture.in.LayoutSources["arm64"] = link
			}

			require.Error(t, fixture.publish(t))
			require.Empty(t, *fixture.gated)
			require.Zero(t, fixture.writes.Load())
			require.NoDirExists(t, fixture.in.OutputDir)
		})
	}
}

func TestParseReleaseImageLayoutSourcesRefusesOneRootForBothArchitectures(t *testing.T) {
	_, err := appcontainer.ParseReleaseImageLayoutSources([]string{"amd64=out/layouts", "arm64=out/layouts/"})
	require.ErrorIs(t, err, errs.ErrUsage)
	require.ErrorContains(t, err, "same root")

	sources, err := appcontainer.ParseReleaseImageLayoutSources([]string{"amd64=out/amd64", "arm64=out/arm64"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"amd64": "out/amd64", "arm64": "out/arm64"}, sources)
}
