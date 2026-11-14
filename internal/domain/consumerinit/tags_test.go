// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package consumerinit_test

import (
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/consumerinit"
)

func TestLatestReleaseTag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		tags []string
		want string
	}{
		{"empty", nil, ""},
		{"catalog", []string{"0.90.0"}, "0.90.0"},
		{"prefixed", []string{"v1.2.0", "v1.10.0"}, "v1.10.0"},
		{"mixed", []string{"v1.9.0", "1.10.0", "v1.2.0"}, "1.10.0"},
		{"equal_versions", []string{"1.2.3", "v1.2.3"}, "v1.2.3"},
		{"exclude_unstable", []string{"2.0.0-rc.1", "v3.0.0+build", "1.0.0+build", "v1.0.0"}, "v1.0.0"},
		{"invalid", []string{"main", "v1", "1.2", "V1.2.3", "01.2.3", "release-request/v1.2.3", " v1.2.3"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := consumerinit.LatestReleaseTag(tc.tags); got != tc.want {
				t.Fatalf("selected %q, want %q", got, tc.want)
			}

			reversed := slices.Clone(tc.tags)
			slices.Reverse(reversed)

			if got := consumerinit.LatestReleaseTag(reversed); got != tc.want {
				t.Fatalf("selection depends on listing order: %q, want %q", got, tc.want)
			}
		})
	}
}
