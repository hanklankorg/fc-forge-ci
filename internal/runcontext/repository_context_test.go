// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package runcontext_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/runcontext"
)

func TestGitLabRepositoryContextPreservesNamespaceAndTrust(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"GITLAB_CI": "true", "CI_PROJECT_PATH": "Group/Sub/My.Repo", "CI_PROJECT_NAMESPACE": "Group/Sub",
		"GITHUB_REPOSITORY": "Wrong/Alias", "GITHUB_REPOSITORY_OWNER": "Wrong",
	}
	for _, tc := range []struct {
		variable runcontext.Var
		want     string
		neutral  string
	}{
		{runcontext.Repository(), "Group/Sub/My.Repo", "REPOSITORY"},
		{runcontext.RepositoryOwner(), "Group/Sub", "REPOSITORY_OWNER"},
	} {
		if got := tc.variable.Resolve(env(values)); got != tc.want {
			t.Fatalf("%s = %q, want %q", tc.variable.Concept, got, tc.want)
		}

		values[tc.neutral] = "explicit/override"
		if got := tc.variable.Resolve(env(values)); got != "explicit/override" {
			t.Fatalf("explicit override lost: %q", got)
		}

		attested, ok := tc.variable.ResolveAttested(env(values))
		if !ok || attested.String() != tc.want {
			t.Fatalf("runner identity lost: %q, %v", attested, ok)
		}
	}
}
