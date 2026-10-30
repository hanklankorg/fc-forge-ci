// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeCargoManifestExplicitVersionOwnsBump(t *testing.T) {
	t.Parallel()

	shared := map[string]any{"version": "9.0.0"}
	pkg := map[string]any{"version": "0.8.6"}
	document := map[string]any{"package": pkg, "workspace": map[string]any{"package": shared}}
	require.NoError(t, normalizeCargoManifest(document, "0.8.6"))
	require.Equal(t, "9.0.0", shared["version"])
	require.NotContains(t, pkg, "version")
}
