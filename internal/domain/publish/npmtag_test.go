// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/publish"
)

func TestNPMVersionTag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ version, tag, want string }{
		{"1.2.3", "release-{version}", "release-1.2.3"},
		{"1.2.3-rc.1", "release-{version}", "release-1.2.3-rc.1"},
		{"1.2.3+build.7", "release-{version}", "release-1.2.3"},
		{"1.2.3+build.7", "release-1.2.3", "release-1.2.3"},
		{"1.2.3", "release-1.2.3", "release-1.2.3"},
		{"1.2.3-rc.1", "release-1.2.3-rc.1", "release-1.2.3-rc.1"},
		{"1.2.3", "", ""},
		{"1.2.3", "latest", ""},
		{"1.2.3-rc.1", "next", ""},
		{"1.2.3-rc.1", "snapshot", ""},
		{"1.2.3", "release-2.0.0", ""},
		{"1.2", "release-{version}", ""},
		{"v1.2.3", "release-{version}", ""},
		{"01.2.3", "release-{version}", ""},
		{"1.2.3\n", "release-{version}", ""},
	} {
		t.Run(tc.version+"/"+tc.tag, func(t *testing.T) {
			t.Parallel()

			got, err := publish.NPMVersionTag(tc.version, tc.tag)
			if tc.want == "" {
				require.ErrorIs(t, err, errs.ErrInvalidConfig)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.want, got)
		})
	}
}
