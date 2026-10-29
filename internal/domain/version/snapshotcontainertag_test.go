// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/stretchr/testify/require"
)

func TestSnapshotContainerTagPostfixBoundsAndBranchCollisions(t *testing.T) {
	tag, err := version.ComposeSnapshotContainerTag("1.2.3", "main", "abc1234")
	require.NoError(t, err)
	require.Regexp(t, `^1\.2\.3-main-[0-9a-f]{16}-abc1234-snapshot$`, tag)
	seen := map[string]bool{}
	for _, branch := range []string{"feature/x", "feature-x", "feature-x-217d2bf51f16e9dc", "feature_x", strings.Repeat("x", 300) + "a", strings.Repeat("x", 300) + "b"} {
		tag, err = version.ComposeSnapshotContainerTag("1.2.3", branch, "abc1234")
		require.NoError(t, err)
		require.LessOrEqual(t, len(tag+"-arm64"), 128)
		require.True(t, strings.HasSuffix(tag, "-abc1234-snapshot"))
		require.False(t, seen[tag], branch)
		seen[tag] = true
	}
	original, err := version.ComposeSnapshotContainerTag("1.2.3", "feature/x", "abc1234")
	require.NoError(t, err)
	impersonator := strings.TrimSuffix(strings.TrimPrefix(original, "1.2.3-"), "-abc1234-snapshot")
	impersonated, err := version.ComposeSnapshotContainerTag("1.2.3", impersonator, "abc1234")
	require.NoError(t, err)
	require.NotEqual(t, original, impersonated)
	_, err = version.ComposeSnapshotContainerTag("1.2.3", "///", "abc1234")
	require.Error(t, err)
	_, err = version.ComposeSnapshotContainerTag(strings.Repeat("1", 120), "main", "abc1234")
	require.Error(t, err)
	// Existing package versions keep their established infix.
	require.Equal(t, "1.2.3-snapshot-feature-x-abc1234", version.ComposeSnapshotVersion("1.2.3", "feature/x", "abc1234"))
}
