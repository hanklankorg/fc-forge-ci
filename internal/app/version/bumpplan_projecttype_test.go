// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

// projectTypePlan is a monorepo plan of a Maven, an npm and a Go project.
func projectTypePlan(t *testing.T) string {
	t.Helper()

	body, err := json.Marshal(pipeline.ReleasePrepareStagePlan{
		Version: pipeline.ReleasePlanVersion,
		Stage:   "prepare",
		Targets: pipeline.ReleasePrepareTargets{VersionBump: pipeline.TargetPlan[pipeline.PlannedArtifact]{
			Runs: true,
			Items: []pipeline.PlannedArtifact{
				{Name: "backend", ProjectType: projecttype.Maven, WorkingDirectory: "backend"},
				{Name: "frontend", ProjectType: projecttype.NPM, WorkingDirectory: "frontend"},
				{Name: "cli", ProjectType: projecttype.Go, WorkingDirectory: "cli"},
			},
		}},
	})
	require.NoError(t, err)

	return string(body)
}

// A family's version job runs its own native tool alone: --project-type npm
// bumps and copies the changelog for the npm project only, and names only its
// paths, so the Maven and Go projects stay for their own jobs.
func TestBumpPlan_ProjectTypesRunOnlyTheirNativeTools(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"backend", "frontend", "cli"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0o755))
	}

	writeFile(t, root, "backend/pom.xml", "<project><version>1.0.0</version></project>\n")
	writeFile(t, root, "frontend/package.json", "{\"name\":\"frontend\",\"version\":\"1.0.0\"}\n")
	writeFile(t, root, "frontend/package-lock.json", "{\"name\":\"frontend\",\"version\":\"1.0.0\",\"lockfileVersion\":3}\n")
	writeFile(t, root, "cli/go.mod", "module example.com/cli\n")
	writeFile(t, root, "full.md", "# Changes\n")
	t.Chdir(root)

	maven, npm, cargo := &fakeMavenOps{}, &fakeNPMOps{}, &fakeCargoOps{avail: true}
	sink := fakeoutputsink.New(t)
	require.NoError(t, appversion.BumpPlan(t.Context(), appversion.BumpOps{Maven: maven, NPM: npm, Cargo: cargo}, sink, io.Discard, io.Discard, output.Annotator{}, appversion.BumpPlanInput{
		PlanJSON: projectTypePlan(t), Version: "1.2.3", ChangelogFile: filepath.Join(root, "full.md"),
		ProjectTypes: []projecttype.Type{projecttype.NPM},
	}))

	assert.Equal(t, 1, npm.calls)
	assert.Equal(t, "frontend", filepath.Base(npm.dir))
	assert.Zero(t, maven.calls)
	assert.Empty(t, cargo.calls)
	assert.FileExists(t, filepath.Join(root, "frontend", "CHANGELOG.md"))
	assert.NoFileExists(t, filepath.Join(root, "backend", "CHANGELOG.md"))
	assert.NoFileExists(t, filepath.Join(root, "cli", "CHANGELOG.md"))
	assert.NotContains(t, sink.Single("file-pattern"), "backend")
	assert.NotContains(t, sink.Single("file-pattern"), "cli")
	assert.Contains(t, sink.Single("file-pattern"), "frontend/package.json")
}

// npm gives npm-shrinkwrap.json precedence over package-lock.json, and a
// patch that leaves out the lockfile npm wrote is refused, so the npm
// family's file pattern names the shrinkwrap file too.
func TestBumpPlan_NPMFilePatternNamesTheShrinkwrapFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "frontend"), 0o755))
	writeFile(t, root, "frontend/package.json", "{\"name\":\"frontend\",\"version\":\"1.0.0\"}\n")
	writeFile(t, root, "frontend/npm-shrinkwrap.json", "{\"name\":\"frontend\",\"version\":\"1.0.0\",\"lockfileVersion\":3}\n")
	writeFile(t, root, "full.md", "# Changes\n")
	t.Chdir(root)

	sink := fakeoutputsink.New(t)
	require.NoError(t, appversion.BumpPlan(t.Context(), appversion.BumpOps{Maven: &fakeMavenOps{}, NPM: &fakeNPMOps{}, Cargo: &fakeCargoOps{avail: true}}, sink, io.Discard, io.Discard, output.Annotator{}, appversion.BumpPlanInput{
		PlanJSON: projectTypePlan(t), Version: "1.2.3", ChangelogFile: filepath.Join(root, "full.md"),
		ProjectTypes: []projecttype.Type{projecttype.NPM},
	}))

	assert.Contains(t, strings.Fields(sink.Single("file-pattern")), "frontend/npm-shrinkwrap.json")
}

func TestBumpPlan_ProjectTypesRefuseAToolLessTypeAndAPatch(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]appversion.BumpPlanInput{
		"no native tool": {Version: "1.2.3", ProjectTypes: []projecttype.Type{projecttype.Go}},
		"with a patch":   {Version: "1.2.3", ProjectTypes: []projecttype.Type{projecttype.NPM}, PatchPath: "bump.patch", PatchSHA256: "00"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			in.PlanJSON = projectTypePlan(t)
			maven, npm := &fakeMavenOps{}, &fakeNPMOps{}
			err := appversion.BumpPlan(t.Context(), appversion.BumpOps{Maven: maven, NPM: npm, Cargo: &fakeCargoOps{avail: true}},
				fakeoutputsink.New(t), io.Discard, io.Discard, output.Annotator{}, in)
			require.ErrorIs(t, err, errs.ErrUsage)
			assert.Zero(t, maven.calls+npm.calls)
		})
	}
}
