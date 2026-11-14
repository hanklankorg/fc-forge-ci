// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

const releaseStageDeclaration = `artifacts:
  - name: backend
    project-type: maven
    working-directory: backend
  - name: frontend
    project-type: npm
    working-directory: frontend
`

// The GitLab release plans what it bumps and builds from the declaration;
// without one, and only for the default path, it plans neither.
func TestLoadReleaseStagePlans_PlansTheDeclarationOrNothing(t *testing.T) {
	t.Parallel()

	declared := fstest.MapFS{".forge-ci/artifacts.yml": {Data: []byte(releaseStageDeclaration)}}

	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: ".forge-ci/artifacts.yml", FS: declared}, output.Annotator{}, false)
	require.NoError(t, err)
	assert.True(t, plans.Prepare.Targets.VersionBump.Runs)
	require.Len(t, plans.Prepare.Targets.VersionBump.Items, 2)
	assert.Equal(t, projecttype.Maven, plans.Prepare.Targets.VersionBump.Items[0].ProjectType)
	assert.True(t, plans.Build.Targets.Maven.Runs)
	assert.True(t, plans.Build.Targets.NPM.Runs)

	absent := appconfig.EmitConfigPlanInput{Path: ".forge-ci/artifacts.yml", FS: fstest.MapFS{}}

	plans, err = appconfig.LoadReleaseStagePlans(absent, output.Annotator{}, true)
	require.NoError(t, err)
	assert.Empty(t, plans.Prepare.Targets.VersionBump.Items)
	assert.False(t, plans.Build.Targets.Maven.Runs)

	_, err = appconfig.LoadReleaseStagePlans(absent, output.Annotator{}, false)
	require.ErrorIs(t, err, errs.ErrMissingInput)
}

// A declaration that is a symbolic link decides no plan, in a repository
// checkout or a file system: the plan must come from the file the
// repository holds, not one a link names elsewhere.
func TestLoadReleaseStagePlans_RefusesALinkedDeclaration(t *testing.T) {
	t.Parallel()

	linked := fstest.MapFS{
		"elsewhere.yml":           {Data: []byte(releaseStageDeclaration)},
		".forge-ci/artifacts.yml": {Data: []byte("../elsewhere.yml"), Mode: fs.ModeSymlink},
	}

	_, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: ".forge-ci/artifacts.yml", FS: linked}, output.Annotator{}, true)
	require.ErrorIs(t, err, errs.ErrMissingInput)
	require.ErrorContains(t, err, "symbolic link")

	dir := t.TempDir()
	target, link := filepath.Join(dir, "elsewhere.yml"), filepath.Join(dir, "artifacts.yml")
	require.NoError(t, os.WriteFile(target, []byte(releaseStageDeclaration), 0o600))
	require.NoError(t, os.Symlink("elsewhere.yml", link))

	_, err = appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: link}, output.Annotator{}, true)
	require.ErrorIs(t, err, errs.ErrMissingInput)
	require.ErrorContains(t, err, "symbolic link")

	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: target}, output.Annotator{}, false)
	require.NoError(t, err)
	assert.True(t, plans.Prepare.Targets.VersionBump.Runs)
}

// A relative declaration is read inside the working directory, the
// repository: a symbolic link above it that leads out of the repository
// is refused, not followed to a file the repository does not hold.
func TestLoadReleaseStagePlans_RefusesADeclarationOutsideTheRepository(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "artifacts.yml"), []byte(releaseStageDeclaration), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(repo, ".forge-ci")))
	t.Chdir(repo)

	_, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: ".forge-ci/artifacts.yml"}, output.Annotator{}, true)
	require.ErrorContains(t, err, "escapes")

	require.NoError(t, os.Remove(filepath.Join(repo, ".forge-ci")))
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".forge-ci"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".forge-ci", "artifacts.yml"), []byte(releaseStageDeclaration), 0o600))

	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: ".forge-ci/artifacts.yml"}, output.Annotator{}, false)
	require.NoError(t, err)
	assert.True(t, plans.Prepare.Targets.VersionBump.Runs)
}
