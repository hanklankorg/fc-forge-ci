// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	plancmd "github.com/diggsweden/forge-ci/internal/cli/commands/plan"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const distroPackagesDeclaration = `artifacts:
  - name: cli
    project-type: go
distro-packages: {}
`

// The GitLab release has no registry for the packages, so its plan refuses
// the block and writes no job. The plan the Forgejo release reads, of the
// same declaration, admits it.
func TestDistroPackagesAreRefusedByTheGitLabPlanAndAdmittedByForgejos(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	declaration, jobs := filepath.Join(dir, "artifacts.yml"), filepath.Join(dir, "families.yml")
	require.NoError(t, os.WriteFile(declaration, []byte(distroPackagesDeclaration), 0o600))

	err := plancmd.New().Run(t.Context(), []string{
		"plan", "gitlab-release-pipeline", "--file", declaration,
		"--runtime-image-go", "registry.example/go@sha256:" + strings.Repeat("1", 64),
		"--output", jobs,
	})
	require.ErrorIs(t, err, errs.ErrUnsupported)
	require.ErrorContains(t, err, "the GitLab release cannot publish .deb, .rpm and .apk files to a registry")
	require.NoFileExists(t, jobs)

	require.NoError(t, plancmd.New().Run(t.Context(), []string{
		"plan", "release-versions", "--file", declaration,
		"--prepare-plan-output", filepath.Join(dir, "prepare.json"),
	}))
}
