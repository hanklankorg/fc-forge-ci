// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/release"
)

func TestSignatureSidecars_IncludesBothGPGAndCosignLayouts(t *testing.T) {
	t.Parallel()
	require.Equal(t,
		[]string{"foo.zip.asc", "foo.zip.bundle"},
		release.SignatureSidecars("foo.zip"),
	)
}
