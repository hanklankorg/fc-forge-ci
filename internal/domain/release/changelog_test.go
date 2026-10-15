// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/release"
)

func TestChangelogSectionSelectsOneVersionAndPreservesLinks(t *testing.T) {
	t.Parallel()

	for _, heading := range []string{"## [1.2.3] - 2026-10-07", "## [v1.2.3](https://example.invalid/tag)", "## v1.2.3"} {
		t.Run(heading, func(t *testing.T) {
			t.Parallel()

			body := "# Changelog\n\n## [Unreleased]\n- future\n\n" + heading + "\n\n### Fixed\n- selected [issue]\n\n```md\n## [1.2.3]\n```\n\n## [1.2.2]\n- older\n\n[issue]: https://example.invalid/issue\n"
			got, err := release.ChangelogSection(body, "v1.2.3")
			require.NoError(t, err)
			require.Equal(t, heading+"\n\n### Fixed\n- selected [issue]\n\n```md\n## [1.2.3]\n```\n\n[issue]: https://example.invalid/issue\n", got)
		})
	}
}

func TestChangelogSectionFailsClosedOnMissingOrAmbiguousVersions(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"## [1.2.30]\nwrong version\n",
		"~~~markdown\n## [1.2.3]\nexample only\n~~~\n",
		"    ## [1.2.3]\n    indented example\n",
		"## [1.2.3]\none\n## [1.2.3]\ntwo\n",
	} {
		_, err := release.ChangelogSection(body, "v1.2.3")
		require.ErrorIs(t, err, errs.ErrValidation)
	}

	_, err := release.ChangelogSection("## [1.2.3]\n", "1.2.3\n## injected")
	require.ErrorIs(t, err, errs.ErrUsage)

	got, err := release.ChangelogSection("## [2.0.0-rc.1]\r\npreview\r\n", "v2.0.0-rc.1")
	require.NoError(t, err)
	require.Equal(t, "## [2.0.0-rc.1]\r\npreview\n", got)
}
