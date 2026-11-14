// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package consumerinit

import (
	"strings"

	"golang.org/x/mod/semver"

	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// LatestReleaseTag selects the highest stable X.Y.Z or vX.Y.Z tag for init.
// Preserve the exact spelling; prefer the v-prefixed tag when both exist so
// adding its unprefixed alias does not change an existing selection.
func LatestReleaseTag(tags []string) string {
	var best, bestVersion string

	for _, tag := range tags {
		parsed, ok := version.ParseSemver(tag)
		if !ok || parsed.Prerelease != "" || parsed.Build != "" {
			continue
		}

		normalized := "v" + parsed.Version

		comparison := semver.Compare(normalized, bestVersion)
		if comparison > 0 || (comparison == 0 && strings.HasPrefix(tag, "v")) {
			best, bestVersion = tag, normalized
		}
	}

	return best
}
