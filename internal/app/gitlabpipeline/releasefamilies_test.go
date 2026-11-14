// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabpipeline_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gitlabpipeline "github.com/diggsweden/forge-ci/internal/app/gitlabpipeline"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// monorepoRelease is the plan of a Maven backend, an npm frontend and a Go
// tool, each bumped and built.
func monorepoRelease() (pipeline.ReleasePrepareStagePlan, pipeline.ReleaseBuildStagePlan) {
	backend := pipeline.PlannedArtifact{Name: "backend", ProjectType: projecttype.Maven, WorkingDirectory: "backend", BuildType: config.BuildTypeApplication}
	frontend := pipeline.PlannedArtifact{Name: "frontend", ProjectType: projecttype.NPM, WorkingDirectory: "frontend"}
	tool := pipeline.PlannedArtifact{Name: "tool", ProjectType: projecttype.Go, WorkingDirectory: "tool"}

	prepare := pipeline.ReleasePrepareStagePlan{Version: 1, Stage: "prepare", Targets: pipeline.ReleasePrepareTargets{
		VersionBump: runningTarget(backend, frontend, tool),
	}}
	build := pipeline.ReleaseBuildStagePlan{Version: 1, Stage: "build", Targets: pipeline.ReleaseBuildTargets{
		Maven: runningTarget(backend), NPM: runningTarget(frontend), Go: runningTarget(tool),
	}}

	return prepare, build
}

// The plan names the jobs: a version job for each family whose projects a
// native tool bumps (Go has none), a build job for each family it builds, and
// each job only extends its hidden job in the pinned template.
func TestPlanReleaseFamilies_NamesThePlannedFamiliesJobs(t *testing.T) {
	t.Parallel()

	prepare, build := monorepoRelease()

	families, err := gitlabpipeline.PlanReleaseFamilies(prepare, build)
	require.NoError(t, err)
	assert.Equal(t, gitlabpipeline.ReleaseFamilies{Version: []string{"java", "node"}, Build: []string{"java", "node", "go"}}, families)
	assert.Equal(t, map[string]any{
		"Prepare: version java": map[string]any{"extends": ".forge-ci-release-version-java"},
		"Prepare: version node": map[string]any{"extends": ".forge-ci-release-version-node"},
		"Build: java":           map[string]any{"extends": ".forge-ci-release-build-java"},
		"Build: node":           map[string]any{"extends": ".forge-ci-release-build-node"},
		"Build: go":             map[string]any{"extends": ".forge-ci-release-build-go"},
	}, families.Jobs())
	assert.Equal(t, []string{"Prepare: version java", "Prepare: version node", "Build: java", "Build: node", "Build: go"}, families.JobNames())

	// An npm project sent to forge-packages adds the package job; a Maven
	// application's entry, which no forge publishes, or an npm target that
	// does not run, adds none.
	build.Targets.NPM.Items[0].PublishTo = []config.PublishTarget{config.PublishForgePackages}
	build.Targets.Maven.Items[0].PublishTo = []config.PublishTarget{config.PublishForgePackages}
	build.Targets.NPM.Items[0].WorkingDirectory = "web"
	families, err = gitlabpipeline.PlanReleaseFamilies(prepare, build)
	require.NoError(t, err)
	assert.Equal(t, []string{"web"}, families.Packages, "the project directories")
	assert.Equal(t, map[string]any{"extends": ".forge-ci-release-publish-npm-packages"}, families.Jobs()["Publish: npm packages"])
	assert.Equal(t, "Publish: npm packages", families.JobNames()[5])

	// A library the release would not publish is refused, not left out.
	build.Targets.Maven.Items[0].BuildType = config.BuildTypeLibrary
	_, err = gitlabpipeline.PlanReleaseFamilies(prepare, build)
	require.ErrorIs(t, err, errs.ErrUnsupported)
	require.ErrorContains(t, err, `maven project "backend" lists forge-packages`)

	build.Targets.Maven.Items[0].BuildType = config.BuildTypeApplication

	build.Targets.NPM.Runs = false
	assert.Empty(t, gitlabpipeline.ReleasePackages(build))
	build.Targets.NPM.Runs = true

	// A plan that does not bump runs no version job.
	prepare.Targets.VersionBump.Runs = false
	families, err = gitlabpipeline.PlanReleaseFamilies(prepare, build)
	require.NoError(t, err)
	assert.Empty(t, families.Version)
}

// GitLab refuses an empty configuration file, so a release without a family
// still writes one hidden key.
func TestReleaseFamilies_JobsOfNoFamilyIsOneHiddenKey(t *testing.T) {
	t.Parallel()

	prepare, build := pipeline.UndeclaredReleaseStagePlans()

	families, err := gitlabpipeline.PlanReleaseFamilies(prepare, build)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{".forge-ci-release-families": map[string]any{"script": []string{"true"}}}, families.Jobs())
}

func TestPlanReleaseFamilies_RefusesWhatNoJobCanRun(t *testing.T) {
	t.Parallel()

	prepare, build := monorepoRelease()

	images := testRuntimeImages
	images.Node = ""

	families, err := gitlabpipeline.PlanReleaseFamilies(prepare, build)
	require.NoError(t, err)
	require.NoError(t, families.RequireImages(testRuntimeImages))
	err = families.RequireImages(images)
	require.ErrorIs(t, err, errs.ErrUsage)
	require.ErrorContains(t, err, "runtime-image-node")

	// The bump alone needs the family's image too.
	build.Targets.NPM = pipeline.TargetPlan[pipeline.PlannedArtifact]{}
	families, err = gitlabpipeline.PlanReleaseFamilies(prepare, build)
	require.NoError(t, err)
	require.ErrorContains(t, families.RequireImages(images), "runtime-image-node")

	build.Targets.XcodeIOS = runningTarget(pipeline.PlannedArtifact{Name: "app", ProjectType: projecttype.XcodeIOS, WorkingDirectory: "ios"})
	_, err = gitlabpipeline.PlanReleaseFamilies(prepare, build)
	require.ErrorIs(t, err, errs.ErrUnsupported)
}

// The base graph is the policy's: a child's plan waits for the promotion of
// the stage that built its parent, its build for the parent job, and the gate
// for every promotion. Each job carries its stage and parent as literals.
func TestBaseStages_OrderChildrenAfterTheirParentsPromotion(t *testing.T) {
	t.Parallel()

	stages, err := gitlabpipeline.ParseBaseStages("base tools+debug:base extra:debug", "buildah")
	require.NoError(t, err)

	families := gitlabpipeline.ReleaseFamilies{Bases: stages}
	jobs := families.Jobs()
	need := func(job string, artifacts bool) map[string]any {
		return map[string]any{"job": job, "artifacts": artifacts}
	}

	assert.Equal(t, []string{
		"Base: plan base", "Base: build base", "Base: publish base", "Base: sign and promote base",
		"Base: plan tools+debug", "Base: parent tools+debug", "Base: build tools+debug", "Base: publish tools+debug", "Base: sign and promote tools+debug",
		"Base: plan extra", "Base: parent extra", "Base: build extra", "Base: publish extra", "Base: sign and promote extra",
		"Base: all promoted",
	}, families.JobNames())
	assert.Len(t, jobs, 15)
	assert.Equal(t, map[string]any{
		"extends": ".forge-ci-release-base-plan", "script": []string{"rc_base_plan 'base' ''"},
		"needs": []any{need("Prepare: release commit and tag", false)},
	}, jobs["Base: plan base"])
	assert.Equal(t, map[string]any{
		"extends": ".forge-ci-release-base-plan", "script": []string{"rc_base_plan 'tools+debug' 'base'"},
		"needs": []any{need("Prepare: release commit and tag", false), need("Base: sign and promote base", true)},
	}, jobs["Base: plan tools+debug"])
	assert.Equal(t, map[string]any{
		"extends": ".forge-ci-release-base-parent", "script": []string{"rc_base_parent 'extra' 'debug'"},
		"needs": []any{need("Base: plan extra", true), need("Base: sign and promote tools+debug", true)},
	}, jobs["Base: parent extra"])
	assert.Equal(t, []any{need("Base: plan extra", true), need("Base: parent extra", true)}, jobs["Base: build extra"].(map[string]any)["needs"])
	assert.Equal(t, []any{need("Base: plan base", true)}, jobs["Base: build base"].(map[string]any)["needs"])
	assert.Equal(t, []any{need("Base: plan extra", true), need("Base: build extra", true), need("Base: parent extra", true)}, jobs["Base: publish extra"].(map[string]any)["needs"])
	assert.Equal(t, []any{need("Base: plan base", true), need("Base: publish base", true)}, jobs["Base: sign and promote base"].(map[string]any)["needs"])
	assert.Equal(t, map[string]any{"extends": ".forge-ci-release-base-gate", "needs": []any{
		need("Base: sign and promote base", true), need("Base: sign and promote tools+debug", true), need("Base: sign and promote extra", true),
	}}, jobs["Base: all promoted"])

	none, err := gitlabpipeline.ParseBaseStages("", "kaniko")
	require.NoError(t, err)
	assert.Empty(t, none)

	for input, refusal := range map[string]string{
		"tools:base":            "no flavor of an earlier stage",
		"tools:base base":       "no flavor of an earlier stage",
		"base+tools":            "a root stage is one flavor",
		"base base":             "twice",
		"base tools+tools:base": "twice",
		"Base":                  "no base flavor",
		"base tools:":           "no base flavor",
		"base 'x:base":          "no base flavor",
	} {
		_, err := gitlabpipeline.ParseBaseStages(input, "buildah")
		require.ErrorIs(t, err, errs.ErrUsage, input)
		require.ErrorContains(t, err, refusal, input)
	}

	// A root builds with either builder; a child with Buildah alone.
	_, err = gitlabpipeline.ParseBaseStages("base", "kaniko")
	require.NoError(t, err)
	_, err = gitlabpipeline.ParseBaseStages("base tools:base", "kaniko")
	require.ErrorIs(t, err, errs.ErrUnsupported)
	require.ErrorContains(t, err, "needs image-builder buildah")
}

func TestReleaseFamilyArtifacts_ListsTheFamilyPlannedArtifacts(t *testing.T) {
	t.Parallel()

	_, build := monorepoRelease()
	build.Targets.Gradle = runningTarget(pipeline.PlannedArtifact{Name: "service", ProjectType: projecttype.Gradle, WorkingDirectory: "service"})

	names, err := gitlabpipeline.ReleaseFamilyArtifacts(build, "java")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"backend", "service"}, names)

	names, err = gitlabpipeline.ReleaseFamilyArtifacts(build, "rust")
	require.NoError(t, err)
	assert.Empty(t, names)

	_, err = gitlabpipeline.ReleaseFamilyArtifacts(build, "python")
	require.ErrorIs(t, err, errs.ErrUsage)
}
