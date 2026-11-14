// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package gitlabpipeline emits GitLab CI child-pipeline YAML from the typed
// release plan. GitLab cannot expand a runtime-computed matrix, so plan-driven
// fan-out is materialised as a child pipeline whose YAML is generated from the
// same plan JSON GitHub feeds to `strategy: matrix` — the GitLab provider's
// rendering of the shared contract, not a meta-DSL.
//
// Because each ecosystem's build is now one thin job (`build <eco> run`, exposed
// as the `templates/build-<eco>.yml` components), the generator just emits one
// `include:` per running plan item, pointing at the matching component with
// per-item inputs. No job bodies are synthesised here.
package gitlabpipeline

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"

	"gopkg.in/yaml.v3"
)

// ChildPipeline is the top-level generated child-pipeline document.
type ChildPipeline struct {
	Stages  []string       `yaml:"stages"`
	Include []IncludeEntry `yaml:"include"`
}

// IncludeEntry includes one Catalog component with per-item inputs. Inputs is a
// map so yaml.v3 emits its keys in deterministic (sorted) order.
type IncludeEntry struct {
	Component string         `yaml:"component"`
	Inputs    map[string]any `yaml:"inputs,omitempty"`
}

// BuildPipelineOptions carries what the plan can't supply: where the components
// live and the release version threaded into each build.
type BuildPipelineOptions struct {
	// ComponentBase is the Catalog path prefix, e.g.
	// "$CI_SERVER_FQDN/diggsweden/forge-ci".
	ComponentBase string
	// ComponentRef pins the component version, e.g. "1.0.0".
	ComponentRef string
	// Version is the release version forwarded to each build component.
	Version string
	// Stage is the child-pipeline stage (default "build").
	Stage string
	// RuntimeImages are the images the build components run in. The
	// components have no default, so each running family needs one.
	RuntimeImages RuntimeImages
}

// RuntimeImages names the forge-ci runtime image of each toolchain family.
type RuntimeImages struct {
	Java    string // build-maven and build-gradle
	Node    string // build-npm
	Go      string // build-go
	Rust    string // build-cargo
	Android string // build-gradle-android
}

func mavenBuildInputs(artifact pipeline.PlannedArtifact, inputs map[string]any) map[string]any {
	inputs["build-type"] = mavenBuildType(artifact.BuildType)
	// Only a library activates a profile, whether or not it has a Maven block.
	if artifact.BuildType == config.BuildTypeLibrary {
		inputs["profile"] = "central-release"
		if artifact.Maven != nil && artifact.Maven.MavenProfile != "" {
			inputs["profile"] = artifact.Maven.MavenProfile
		}
	}

	if artifact.Maven != nil {
		inputs["java-version"] = artifact.Maven.JavaVersion
		if artifact.Maven.PrivateDependencies {
			inputs["private-dependencies"] = true
		}

		if artifact.Maven.SettingsPath != "" {
			inputs["settings-path"] = artifact.Maven.SettingsPath
		}
	}

	return inputs
}

// BuildStagePipeline renders a child pipeline that fans the release build stage
// out over its running targets and items: one `include:` of the matching
// `build-<eco>` component per item. It refuses targets that have no Catalog
// component instead of silently dropping planned work.
func BuildStagePipeline(plan pipeline.ReleaseBuildStagePlan, opts BuildPipelineOptions) (ChildPipeline, error) {
	if err := validateComponentCoordinate(opts); err != nil {
		return ChildPipeline{}, err
	}

	tgt := plan.Targets
	if err := validateBuildStageTargets(tgt, opts.Version); err != nil {
		return ChildPipeline{}, err
	}

	stage := opts.Stage
	if stage == "" {
		stage = stageBuild
	}

	out := ChildPipeline{Stages: []string{stage}}

	// Each running target contributes one component include per item. Go and
	// Cargo components also take the artifact name and release version.
	images := opts.RuntimeImages

	var missingImage error

	for _, include := range buildFamilies(tgt, images, opts.Version) {
		err := addTargetIncludes(&out, include.component, include.target, opts, artifactName, include.binaries, include.binaries, func(artifact pipeline.PlannedArtifact) map[string]any {
			return include.inputs(artifact, map[string]any{
				"working-directory": dirOrDot(artifact.WorkingDirectory),
				"enable-build-sbom": hasBuildSBOM(artifact),
				"runtime-image":     include.image,
			})
		})
		if err != nil {
			return ChildPipeline{}, err
		}

		if include.target.Runs && strings.TrimSpace(include.image) == "" && missingImage == nil {
			missingImage = fmt.Errorf("the plan runs %s, which needs the %s runtime image: %w", include.component, include.family, errs.ErrUsage)
		}
	}

	if err := out.validate(); err != nil {
		return ChildPipeline{}, err
	}

	if missingImage != nil {
		return ChildPipeline{}, missingImage
	}

	return out, nil
}

// buildFamily is one target of the release build stage: the Catalog
// component that builds it, the toolchain family whose runtime image it runs
// in, and the component's per-item inputs.
type buildFamily struct {
	component string
	target    pipeline.TargetPlan[pipeline.PlannedArtifact]
	binaries  bool
	family    string
	image     string
	inputs    func(pipeline.PlannedArtifact, map[string]any) map[string]any
}

// buildFamilies is every build-stage target in the order the components are
// included, with the runtime image of its family. It is the one mapping of
// targets to families, for the gate's components and the release's jobs.
func buildFamilies(tgt pipeline.ReleaseBuildTargets, images RuntimeImages, version string) []buildFamily {
	return []buildFamily{
		{"build-maven", tgt.Maven, false, familyJava, images.Java, mavenBuildInputs},
		{"build-npm", tgt.NPM, false, familyNode, images.Node, npmBuildInputs},
		{"build-gradle", tgt.Gradle, false, familyJava, images.Java, func(artifact pipeline.PlannedArtifact, inputs map[string]any) map[string]any {
			return gradleBuildInputs(artifact, inputs, version)
		}},
		{"build-gradle-android", tgt.GradleAndroid, false, familyAndroid, images.Android, func(artifact pipeline.PlannedArtifact, inputs map[string]any) map[string]any {
			return gradleAndroidBuildInputs(artifact, inputs, version)
		}},
		{"build-go", tgt.Go, true, familyGo, images.Go, goBuildInputs},
		{"build-cargo", tgt.Cargo, true, familyRust, images.Rust, cargoBuildInputs},
	}
}

// The toolchain families, each one runtime image.
const (
	familyJava    = "java"
	familyNode    = "node"
	familyGo      = "go"
	familyRust    = "rust"
	familyAndroid = "android"
)

// validateBuildStageTargets refuses targets the Catalog components cannot
// express before any include is rendered.
func validateBuildStageTargets(tgt pipeline.ReleaseBuildTargets, version string) error {
	for _, contract := range []struct {
		component   string
		projectType projecttype.Type
		target      pipeline.TargetPlan[pipeline.PlannedArtifact]
	}{
		{component: "build-maven", projectType: projecttype.Maven, target: tgt.Maven},
		{component: "build-npm", projectType: projecttype.NPM, target: tgt.NPM},
		{component: "build-gradle", projectType: projecttype.Gradle, target: tgt.Gradle},
		{component: "build-gradle-android", projectType: projecttype.GradleAndroid, target: tgt.GradleAndroid},
		{component: "build-go", projectType: projecttype.Go, target: tgt.Go},
		{component: "build-cargo", projectType: projecttype.Cargo, target: tgt.Cargo},
	} {
		if err := validateArtifactTarget(contract.component, contract.target, contract.projectType); err != nil {
			return err
		}
	}

	if targetPlanned(tgt.XcodeIOS) {
		return unsupportedTarget(stageBuild, "xcode-ios", "build-xcode-ios")
	}

	if err := validateGoTargetRepresentable(tgt.Go); err != nil {
		return err
	}

	for _, artifact := range slices.Concat(tgt.Gradle.Items, tgt.GradleAndroid.Items) {
		if artifact.PublicationArtifactName != "" && version == "" {
			return fmt.Errorf("gradle publication export requires an explicit release version before generating a pipeline: %w", errs.ErrInvalidConfig)
		}
	}

	return validateCargoTargetRepresentable(tgt.Cargo)
}

func npmBuildInputs(artifact pipeline.PlannedArtifact, inputs map[string]any) map[string]any {
	if artifact.NPM != nil {
		inputs["node-version"] = artifact.NPM.NodeVersion
		if artifact.NPM.PrivateDependencies {
			inputs["private-dependencies"] = true
			inputs["dependency-registry"] = artifact.NPM.DependencyRegistry
		}
	}

	return inputs
}

func gradleBuildInputs(artifact pipeline.PlannedArtifact, inputs map[string]any, version string) map[string]any {
	if artifact.Gradle != nil {
		inputs["gradle-tasks"] = artifact.Gradle.GradleTasks

		inputs["java-version"] = artifact.Gradle.JavaVersion
		if artifact.Gradle.PrivateDependencies {
			inputs["private-dependencies"] = true
		}
	}

	return gradlePublicationInputs(artifact, inputs, version)
}

func gradleAndroidBuildInputs(artifact pipeline.PlannedArtifact, inputs map[string]any, version string) map[string]any {
	if artifact.BuildType == config.BuildTypeLibrary {
		inputs["library"] = true
	}

	inputs = gradlePublicationInputs(artifact, inputs, version)

	if artifact.GradleAndroid == nil {
		return inputs
	}

	android := artifact.GradleAndroid
	inputs["build-module"] = android.BuildModule
	inputs["product-flavor"] = android.ProductFlavor
	inputs["build-types"] = android.BuildTypes
	inputs["include-aab"] = artifact.BuildType != config.BuildTypeLibrary && (android.IncludeAAB == nil || *android.IncludeAAB)
	inputs["enable-signing"] = artifact.BuildType != config.BuildTypeLibrary && android.EnableAndroidSigning != nil && *android.EnableAndroidSigning

	return inputs
}

// gradlePublicationInputs asks a Gradle build to export its publications for
// the release version when the artifact publishes them.
func gradlePublicationInputs(artifact pipeline.PlannedArtifact, inputs map[string]any, version string) map[string]any {
	if artifact.PublicationArtifactName != "" {
		inputs["export-publications"] = true
		inputs["version"] = version
		inputs["central-publication"] = slices.Contains(artifact.PublishTo, config.PublishMavenCentral)
	}

	return inputs
}

func goBuildInputs(artifact pipeline.PlannedArtifact, inputs map[string]any) map[string]any {
	platforms := "linux/amd64,linux/arm64,darwin/amd64,darwin/arm64"

	if artifact.Go != nil {
		if artifact.Go.Platforms != "" {
			platforms = artifact.Go.Platforms
		}

		inputs["skip-tests"] = artifact.Go.SkipTests

		if artifact.Go.MainPackage != "" {
			inputs["main-package"] = artifact.Go.MainPackage
		}
	}

	inputs["platforms"] = platforms

	return inputs
}

func cargoBuildInputs(artifact pipeline.PlannedArtifact, inputs map[string]any) map[string]any {
	platforms := "linux/amd64"

	if artifact.Cargo != nil {
		if artifact.Cargo.Platforms != "" {
			platforms = artifact.Cargo.Platforms
		}

		inputs["skip-tests"] = artifact.Cargo.SkipTests
	}

	inputs["platforms"] = platforms

	return inputs
}

// artifactName extracts the plan item's name (works for PlannedArtifact).
func artifactName(artifact pipeline.PlannedArtifact) string { return artifact.Name }

// Marshal renders the child pipeline to YAML bytes.
func (c ChildPipeline) Marshal() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}

	body, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal child pipeline: %w", err)
	}

	return body, nil
}

func (c ChildPipeline) validate() error {
	if len(c.Include) == 0 {
		return fmt.Errorf("GitLab child pipeline has no generated jobs: %w", errs.ErrInvalidConfig)
	}

	return nil
}

// addTargetIncludes appends one component include per running item of a target.
// Generic over the item type so build and publish PlannedArtifact targets share
// one fan-out helper. artifact-name and version are emitted only when the
// selected component declares them; extra carries target-specific inputs.
func addTargetIncludes[T any](
	out *ChildPipeline,
	component string,
	target pipeline.TargetPlan[T],
	opts BuildPipelineOptions,
	nameOf func(T) string,
	includeArtifactName, includeVersion bool,
	extra func(T) map[string]any,
) error {
	if err := validateTargetContract(component, target); err != nil {
		return err
	}

	if !target.Runs {
		return nil
	}

	for _, item := range target.Items {
		name := strings.TrimSpace(nameOf(item))
		if name == "" {
			return fmt.Errorf("GitLab %s target contains an unnamed item: %w", component, errs.ErrInvalidConfig)
		}

		jobName := component + "-" + sanitizeJobName(name)
		for _, include := range out.Include {
			if include.Inputs["job-name"] == jobName {
				return fmt.Errorf("GitLab generated duplicate job name %q after sanitizing item %q: %w", jobName, name, errs.ErrInvalidConfig)
			}
		}

		inputs := map[string]any{
			"job-name": jobName,
			keyStage:   out.Stages[0],
		}

		if includeArtifactName {
			inputs["artifact-name"] = name
		}

		if includeVersion && opts.Version != "" {
			inputs["version"] = opts.Version
		}

		if extra != nil {
			for key, value := range extra(item) {
				if text, ok := value.(string); ok && text == "" {
					continue
				}

				inputs[key] = value
			}
		}

		out.Include = append(out.Include, IncludeEntry{
			Component: fmt.Sprintf("%s/%s@%s", opts.ComponentBase, component, opts.ComponentRef),
			Inputs:    inputs,
		})
	}

	return nil
}

func validateTargetContract[T any](component string, target pipeline.TargetPlan[T]) error {
	if target.Runs != (len(target.Items) > 0) {
		return fmt.Errorf("GitLab %s target has inconsistent runs/items contract: %w", component, errs.ErrInvalidConfig)
	}

	return nil
}

func validateComponentCoordinate(opts BuildPipelineOptions) error {
	if strings.TrimSpace(opts.ComponentBase) == "" {
		return fmt.Errorf("GitLab component catalogue coordinate is required: %w", errs.ErrUsage)
	}

	if strings.TrimSpace(opts.ComponentRef) == "" {
		return fmt.Errorf("GitLab component ref is required: %w", errs.ErrUsage)
	}

	// Both are pasted into "<base>/<component>@<ref>", which cannot contain
	// whitespace; a padded value would generate includes GitLab cannot resolve.
	if strings.ContainsFunc(opts.ComponentBase+opts.ComponentRef, unicode.IsSpace) {
		return fmt.Errorf("GitLab component coordinate and ref must not contain whitespace: %w", errs.ErrUsage)
	}

	return nil
}

func targetPlanned[T any](target pipeline.TargetPlan[T]) bool {
	return target.Runs || len(target.Items) > 0
}

func validateArtifactTarget(component string, target pipeline.TargetPlan[pipeline.PlannedArtifact], want projecttype.Type) error {
	for _, artifact := range target.Items {
		if artifact.ProjectType != want {
			return fmt.Errorf("GitLab %s target item %q has project type %q, want %q: %w", component, artifact.Name, artifact.ProjectType, want, errs.ErrInvalidConfig)
		}
	}

	return nil
}

// validateGoTargetRepresentable refuses a planned Go artifact whose settings
// the Catalog's build-go component cannot express. The component builds the
// selected main package artifact-first with the artifact's own name. A distinct
// binary name, build tags or ldflags would be silently dropped from the pipeline.
func validateGoTargetRepresentable(target pipeline.TargetPlan[pipeline.PlannedArtifact]) error {
	for _, artifact := range target.Items {
		if artifact.GoBuildMode != "" && artifact.GoBuildMode != config.GoBuildModeArtifactFirst {
			return fmt.Errorf("GitLab build target go contains non-artifact-first build mode %q: %w", artifact.GoBuildMode, errs.ErrInvalidConfig)
		}

		if artifact.Go == nil {
			continue
		}

		if (artifact.Go.BinaryName != "" && artifact.Go.BinaryName != artifact.Name) ||
			artifact.Go.BuildTags != "" || artifact.Go.LDFlags != "" {
			return fmt.Errorf("GitLab build target go cannot represent a distinct binary-name, build-tags, or ldflags: %w", errs.ErrUnsupported)
		}

		if artifact.Go.BuildMode != "" && artifact.Go.BuildMode != config.GoBuildModeArtifactFirst {
			return fmt.Errorf("GitLab build target go config contains non-artifact-first build mode %q: %w", artifact.Go.BuildMode, errs.ErrInvalidConfig)
		}
	}

	return nil
}

// validateCargoTargetRepresentable is validateGoTargetRepresentable's Cargo
// sibling: the build-cargo component names the binary after the artifact and
// builds artifact-first, so a divergent binary name or build mode has nowhere
// to go in the generated pipeline.
func validateCargoTargetRepresentable(target pipeline.TargetPlan[pipeline.PlannedArtifact]) error {
	for _, artifact := range target.Items {
		if artifact.CargoBuildMode != "" && artifact.CargoBuildMode != config.CargoBuildModeArtifactFirst {
			return fmt.Errorf("GitLab build target cargo contains non-artifact-first build mode %q: %w", artifact.CargoBuildMode, errs.ErrInvalidConfig)
		}

		if artifact.Cargo == nil {
			continue
		}

		if artifact.Cargo.BinaryName != "" && artifact.Cargo.BinaryName != artifact.Name {
			return fmt.Errorf("GitLab build target cargo cannot represent a binary-name distinct from artifact-name: %w", errs.ErrUnsupported)
		}

		if artifact.Cargo.BuildMode != "" && artifact.Cargo.BuildMode != config.CargoBuildModeArtifactFirst {
			return fmt.Errorf("GitLab build target cargo config contains non-artifact-first build mode %q: %w", artifact.Cargo.BuildMode, errs.ErrInvalidConfig)
		}
	}

	return nil
}

func unsupportedTarget(stage, target, component string) error {
	return fmt.Errorf("GitLab %s target %q cannot be generated: Catalog component %q is unavailable: %w", stage, target, component, errs.ErrUnsupported)
}

func hasBuildSBOM(a pipeline.PlannedArtifact) bool {
	return slices.Contains(a.EffectiveSBOMs, config.SBOMLayerBuild)
}

// mavenBuildType maps the config enum (application/library) to the maven
// component's app/lib input. Empty defaults to app.
func mavenBuildType(buildType config.BuildType) string {
	if buildType == config.BuildTypeLibrary {
		return "lib"
	}

	return "app"
}

func dirOrDot(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return "."
	}

	return dir
}

// sanitizeJobName makes an artifact name safe as a GitLab job-name suffix.
func sanitizeJobName(name string) string {
	var buf strings.Builder

	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			buf.WriteRune(r)
		default:
			buf.WriteByte('-')
		}
	}

	out := strings.Trim(buf.String(), "-")
	if out == "" {
		return "artifact"
	}

	return out
}
