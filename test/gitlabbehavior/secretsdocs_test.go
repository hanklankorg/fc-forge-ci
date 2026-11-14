// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// A pipeline variable outranks the predefined variables the release jobs
// send their tokens to, so the release's secrets hold only while no one may
// set one. Until the jobs bind the server to a literal input, the project
// setting is the control, and the secrets section requires it, naming each
// host variable it protects.
func TestSecretsSectionRestrictsPipelineVariables(t *testing.T) {
	t.Parallel()

	text := string(reporoot.ReadFile(t, "docs/gitlab.md"))

	_, section, found := strings.Cut(text, "\n## Secrets and tokens\n")
	section, _, _ = strings.Cut(section, "\n## ")

	if !found {
		t.Fatal("docs/gitlab.md has no Secrets and tokens section")
	}

	for _, required := range []string{"ci_pipeline_variables_minimum_override_role: no_one_allowed", "`CI_SERVER_URL`", "`CI_API_V4_URL`", "`CI_REGISTRY`"} {
		if !strings.Contains(section, required) {
			t.Errorf("the secrets section does not name %s", required)
		}
	}
}
