// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/version"
)

func TestReleasePolicyAdmission(t *testing.T) {
	for _, tc := range []struct {
		policy, tag string
		accepted    bool
	}{
		{"", "v1.0.0", true},
		{"", "v1.0.0-beta.2", false},
		{"stable", "v1.0.0", true},
		{"stable", "v1.0.0-rc.1", false},
		{"stable-prerelease", "v1.0.0", true},
		{"stable-prerelease", "v1.0.0-beta.2", true},
		{"stable-prerelease", "v1.0.0-rc.1", true},
		{"stable-prerelease", "v1.0.0-beta.02", false},
		{"stable-prerelease", "v1.0.0+build", false},
		{"stable-prerelease", "v1.0.0-beta.2+build", false},
		{"stable-prerelease", "v1.0", false},
		{"stable-prerelease", "1.0.0", false},
		{"auto", "v1.0.0", false},
	} {
		t.Run(tc.policy+"/"+tc.tag, func(t *testing.T) {
			_, err := version.ValidateReleaseTag(tc.tag, tc.policy)
			if (err == nil) != tc.accepted {
				t.Fatalf("accepted=%v: %v", tc.accepted, err)
			}
		})
	}
}

func TestLatestReleaseTagUsesSemverAndPolicy(t *testing.T) {
	tags := []string{"v1.0.0-beta.2", "v1.0.0-beta.10", "v0.9.0", "v2.0.0+build", "release-request/v3.0.0"}
	if got := version.LatestReleaseTag(tags, "stable-prerelease"); got != "v1.0.0-beta.10" {
		t.Fatalf("latest prerelease = %s", got)
	}

	if got := version.LatestReleaseTag(tags, "stable"); got != "v0.9.0" {
		t.Fatalf("latest stable = %s", got)
	}
}
