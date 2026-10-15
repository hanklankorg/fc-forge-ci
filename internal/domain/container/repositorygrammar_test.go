// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

func TestRepositoryGrammarIsSharedByAllReferenceForms(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("a", 64)

	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"app", true}, {"app.v2", true}, {"app_tools", true}, {"app__tools", true}, {"app---tools", true},
		{".app", false}, {"_app", false}, {"-app", false}, {"app.", false}, {"app-", false},
		{"app..v2", false}, {"app___v2", false}, {"app.-v2", false}, {"App", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			base := "registry.example/org/" + tc.name

			results := []bool{
				container.ValidRepositoryName(base),
				container.ValidTaggedRef(base + ":Release_1"),
				container.ValidDigestPinnedRef(base + "@" + digest),
				container.ValidDigestReference(base + ":Release_1@" + digest),
			}
			for _, accepted := range results {
				if accepted != tc.valid {
					t.Fatalf("reference forms for %q: %v, want %v", base, results, tc.valid)
				}
			}
		})
	}
}
