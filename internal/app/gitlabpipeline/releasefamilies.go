// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabpipeline

import (
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// ReleaseFamilies are the GitLab release child's plan-driven jobs. The child
// pipeline is templates/release-jobs.yml, which defines every job body, as
// a hidden job per family, beside this generated file, which only names the
// jobs the plan runs: one "Prepare: version <family>" per toolchain family
// whose projects the plan bumps with their native tool, and one
// "Build: <family>" per family the plan builds, "Publish: npm packages"
// when it sends an npm project to forge-packages, and the jobs of each stage
// of the policy's base image graph. The file carries no consumer string, and
// of the policy the base stages' names and order alone, so what a job may do
// stays in the pinned template.
type ReleaseFamilies struct {
	// Version are the families whose native tools prepare the version bump.
	Version []string
	// Build are the families whose build jobs run the release build commands.
	Build []string
	// Packages are the directories of the npm projects the release publishes
	// to the project's package registry (ReleasePackages).
	Packages []string
	// Bases are the stages of the base image graph (ParseBaseStages), which
	// the caller sets from the policy's base-stages input.
	Bases []BaseStage
}

// jobPublishNPMPackages is the job that publishes the release's npm packages.
const jobPublishNPMPackages = "Publish: npm packages"

// releaseFamilyOrder is the order families are listed and their jobs named.
var releaseFamilyOrder = []string{familyJava, familyNode, familyGo, familyRust, familyAndroid}

// PlanReleaseFamilies selects the release child's families from the plan and
// refuses a target no GitLab runner builds. The jobs that run them need the
// families' images (RequireImages); a job that only checks which ran does
// not.
func PlanReleaseFamilies(prepare pipeline.ReleasePrepareStagePlan, build pipeline.ReleaseBuildStagePlan) (ReleaseFamilies, error) {
	if targetPlanned(build.Targets.XcodeIOS) {
		return ReleaseFamilies{}, fmt.Errorf("the GitLab release has no macOS runner for the planned xcode-ios build: %w", errs.ErrUnsupported)
	}

	if err := refuseUnpublishedPackages(build.Targets); err != nil {
		return ReleaseFamilies{}, err
	}

	version, buildSet := map[string]bool{}, map[string]bool{}

	// The plan names the families whose native tools prepare its bump.
	for _, family := range prepare.VersionFamilies() {
		version[family.Family] = true
	}

	for _, target := range buildFamilies(build.Targets, RuntimeImages{}, "") {
		if err := validateTargetContract(target.component, target.target); err != nil {
			return ReleaseFamilies{}, err
		}

		if target.target.Runs {
			buildSet[target.family] = true
		}
	}

	out := ReleaseFamilies{Packages: ReleasePackages(build)}

	for _, family := range releaseFamilyOrder {
		if version[family] {
			out.Version = append(out.Version, family)
		}

		if buildSet[family] {
			out.Build = append(out.Build, family)
		}
	}

	return out, nil
}

// RequireImages refuses a planned family the policy gives no runtime image.
func (f ReleaseFamilies) RequireImages(images RuntimeImages) error {
	imageOf := map[string]string{familyJava: images.Java, familyNode: images.Node, familyGo: images.Go, familyRust: images.Rust, familyAndroid: images.Android}

	for _, family := range releaseFamilyOrder {
		if (slices.Contains(f.Version, family) || slices.Contains(f.Build, family)) && strings.TrimSpace(imageOf[family]) == "" {
			return fmt.Errorf("the plan has %s projects, which need the runtime-image-%s input: %w", family, family, errs.ErrUsage)
		}
	}

	return nil
}

// JobNames are the names of the generated jobs, version jobs first, each in
// family order, then the base stages' jobs, and the package job last.
func (f ReleaseFamilies) JobNames() []string {
	names := make([]string, 0, len(f.Version)+len(f.Build)+1)

	for _, family := range f.Version {
		names = append(names, versionJobName(family))
	}

	for _, family := range f.Build {
		names = append(names, buildJobName(family))
	}

	names = append(names, baseJobNames(f.Bases)...)

	if len(f.Packages) > 0 {
		names = append(names, jobPublishNPMPackages)
	}

	return names
}

func versionJobName(family string) string { return "Prepare: version " + family }

func buildJobName(family string) string { return "Build: " + family }

// Jobs renders the generated part of the release child: each job extends its
// family's hidden job of templates/release-jobs.yml. Without a family the file
// holds one hidden job, since GitLab refuses an empty configuration file and
// a job key without a body.
func (f ReleaseFamilies) Jobs() map[string]any {
	jobs := map[string]any{}

	for _, family := range f.Version {
		jobs[versionJobName(family)] = map[string]any{keyExtends: ".forge-ci-release-version-" + family}
	}

	for _, family := range f.Build {
		jobs[buildJobName(family)] = map[string]any{keyExtends: ".forge-ci-release-build-" + family}
	}

	if len(f.Packages) > 0 {
		jobs[jobPublishNPMPackages] = map[string]any{keyExtends: ".forge-ci-release-publish-npm-packages"}
	}

	addBaseJobs(jobs, f.Bases)

	if len(jobs) == 0 {
		jobs[".forge-ci-release-families"] = map[string]any{keyScript: []string{"true"}}
	}

	return jobs
}

// ReleaseFamilyArtifacts are the names of the artifacts a family's build job
// builds, in plan order.
func ReleaseFamilyArtifacts(build pipeline.ReleaseBuildStagePlan, family string) ([]string, error) {
	if !slices.Contains(releaseFamilyOrder, family) {
		return nil, fmt.Errorf("unknown release build family %q: %w", family, errs.ErrUsage)
	}

	var names []string

	for _, target := range buildFamilies(build.Targets, RuntimeImages{}, "") {
		if target.family != family || !target.target.Runs {
			continue
		}

		for _, item := range target.target.Items {
			names = append(names, item.Name)
		}
	}

	return names, nil
}

// ReleasePackages are the directories of the npm projects the release
// publishes to the project's package registry, in plan order: the built npm
// projects that list forge-packages under publish-to. The release binds each
// packed tarball to its project's package.json by them.
func ReleasePackages(build pipeline.ReleaseBuildStagePlan) []string {
	if !build.Targets.NPM.Runs {
		return nil
	}

	var dirs []string

	for _, item := range build.Targets.NPM.Items {
		if slices.Contains(item.PublishTo, config.PublishForgePackages) {
			dirs = append(dirs, item.WorkingDirectory)
		}
	}

	return dirs
}

// refuseUnpublishedPackages refuses a Maven or Gradle project that lists
// forge-packages: the GitLab release publishes npm packages alone, and would
// otherwise release without the package the declaration asks for. A Maven
// application's forge-packages entry is never published, on any forge.
func refuseUnpublishedPackages(targets pipeline.ReleaseBuildTargets) error {
	for _, target := range []pipeline.TargetPlan[pipeline.PlannedArtifact]{targets.Maven, targets.Gradle, targets.GradleAndroid} {
		if !target.Runs {
			continue
		}

		for _, item := range target.Items {
			if !slices.Contains(item.PublishTo, config.PublishForgePackages) || item.BuildType == config.BuildTypeApplication {
				continue
			}

			return fmt.Errorf("the GitLab release publishes npm packages alone, but %s project %q lists forge-packages under publish-to; remove it there and publish the package with the publish-forge-packages component: %w", item.ProjectType, item.Name, errs.ErrUnsupported)
		}
	}

	return nil
}
