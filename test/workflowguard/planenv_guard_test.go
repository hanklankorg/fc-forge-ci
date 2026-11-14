// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// planEnvUse matches a GitHub workflow that names a plan file through the
// environment: FORGE_CI_PLAN set to something, or read by a command.
var planEnvUse = regexp.MustCompile(`FORGE_CI_PLAN:[ \t]*[^"'\s]|FORGE_CI_PLAN:[ \t]*["'][^"']|\$\{?FORGE_CI_PLAN`)

// The launcher a GitHub job installs clears FORGE_CI_PLAN before every
// command (scripts/ci/trusted-bootstrap.sh), so no earlier step can point a
// command at a plan. A workflow that set the variable would write its plan to
// one path and read none: the container build did, and every hosted release
// with an image failed on a missing --mode. A step takes its inputs from its
// own env, and a plan file is named by a literal path.
func TestGitHubWorkflowsNameNoPlanThroughTheEnvironment(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(reporoot.Path(t), ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)

		require.Emptyf(t, planEnvUse.FindAllString(string(body), -1),
			"%s names a plan through FORGE_CI_PLAN, which the launcher clears", entry.Name())
	}
}
