// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPublicTransparencyRequiresNonPublicConsent(t *testing.T) {
	t.Parallel()

	for _, visibility := range []string{"public", "private", "internal", "unknown", ""} {
		for _, explicit := range []bool{false, true} {
			err := release.ValidateTransparencyConsent(visibility, release.TransparencyPublic, explicit, true)
			if visibility == "public" || explicit {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}

			require.NoError(t, release.ValidateTransparencyConsent(visibility, release.TransparencyNone, explicit, true))
			require.NoError(t, release.ValidateTransparencyConsent(visibility, "", explicit, true))
			require.NoError(t, release.ValidateTransparencyConsent(visibility, release.TransparencyPublic, explicit, false))
		}
	}

	require.Error(t, release.ValidateTransparencyConsent("publci", release.TransparencyPublic, true, true))
}
