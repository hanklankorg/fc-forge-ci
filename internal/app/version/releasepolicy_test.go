// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"io"
	"testing"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
)

func TestPrereleaseTagCreationRequiresExplicitPolicy(t *testing.T) {
	for _, policy := range []string{"", "stable", "stable-prerelease", "auto"} {
		t.Run(policy, func(t *testing.T) {
			repo := &fakeTagReleaseRepo{}

			_, err := appversion.TagRelease(t.Context(), repo, appversion.TagReleaseInput{ReleasePolicy: policy, Tag: "v1.0.0-beta.2", Signed: true}, nil, io.Discard)
			if policy == "stable-prerelease" {
				if err != nil || repo.created != "v1.0.0-beta.2" || !repo.createdSign || repo.pushed != repo.created {
					t.Fatalf("signed beta tag not created: %v", err)
				}
			} else if err == nil || repo.created != "" || repo.pushed != "" {
				t.Fatalf("unapproved beta mutated refs: %v", err)
			}
		})
	}
}
