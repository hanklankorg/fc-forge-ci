// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/repository"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
	domainsummary "github.com/diggsweden/forge-ci/internal/domain/summary"
)

// EmitConfigPlanInput drives `config parse-artifacts`.
type EmitConfigPlanInput struct {
	Path string // path to artifacts.yml
	// FS overrides read access for tests. When nil, Path is read from the real OS filesystem.
	FS fs.FS
}

// EmitConfigPlan reads + validates artifacts.yml, derives the typed
// config-plan contract, and writes it through OutputSink + SummarySink.
func EmitConfigPlan(
	ctx context.Context,
	sink ci.OutputSink,
	summary ci.SummarySink,
	stderr io.Writer,
	annot output.Annotator,
	in EmitConfigPlanInput,
) error {
	plan, err := loadConfigPlan(in, annot)
	if err != nil {
		return err
	}

	if err := emitValueOutput(ctx, sink, "config-plan-json", plan); err != nil {
		return err
	}

	// Markdown summary block, rendered from the same plan so its defaults
	// (working directory, Containerfile) are the ones the pipeline uses.
	if summary != nil {
		if err := summary.Append(ctx, renderConfigSummary(plan)); err != nil {
			return err
		}
	}

	return nil
}

// loadConfigPlan reads, validates and derives artifacts.yml into the typed
// plan every consumer of the declaration reads.
func loadConfigPlan(in EmitConfigPlanInput, annot output.Annotator) (pipeline.ConfigPlan, error) {
	cfg, err := loadPlannableConfig(in, annot)
	if err != nil {
		return pipeline.ConfigPlan{}, err
	}

	return pipeline.NewConfigPlan(cfg), nil
}

// loadPlannableConfig reads, validates and derives artifacts.yml, the
// declaration a plan is made from.
func loadPlannableConfig(in EmitConfigPlanInput, annot output.Annotator) (*config.Config, error) {
	if in.Path == "" {
		return nil, fmt.Errorf("artifacts.yml path is required: pass --file <path> or set $ARTIFACTS_CONFIG: %w", errs.ErrUsage)
	}

	cfg, err := loadConfig(in, annot)
	if err != nil {
		return nil, err
	}

	if len(cfg.Artifacts) == 0 && len(cfg.Containers) == 0 {
		return nil, fmt.Errorf("no artifacts or containers found in %s: %w", in.Path, errs.ErrInvalidConfig)
	}

	if err := config.Validate(cfg); err != nil {
		// No extra ErrInvalidConfig wrap: ValidationError already unwraps to
		// it, and double-wrapping printed "invalid configuration" twice.
		return nil, err
	}

	for _, w := range config.Warnings(cfg) {
		annot.Warningf("%s", w)
	}

	if err := config.Derive(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// EmitReleaseImagesPlanInput drives `container release-images plan`.
type EmitReleaseImagesPlanInput struct {
	Config EmitConfigPlanInput
	// DefaultWhenAbsent releases the default image when Config.Path does not
	// exist: one container named after the repository, built from the root
	// Containerfile with every default. A path the caller named must exist.
	DefaultWhenAbsent bool
	Images            pipeline.ReleaseImagesInput
	// ResolvedPlan is optional consumer-owned policy, in the same output shape
	// as the declarative planner. It is admitted before emitting any outputs.
	ResolvedPlan string
	// Runners is the runner inputs the forge layer passed; a build of a
	// platform whose input has no value is refused. Nil is not checked.
	Runners *runners.Inputs
}

// EmitReleaseImagesPlan writes the release-images build and publication
// matrices of the declared containers as the builds and images outputs.
func EmitReleaseImagesPlan(ctx context.Context, sink ci.OutputSink, annot output.Annotator, in EmitReleaseImagesPlanInput) (pipeline.ReleaseImagesPlan, error) {
	images, err := resolveReleaseImagesPlan(in, annot)
	if err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	if err := emitValueOutput(ctx, sink, "builds", images.Builds); err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	if err := emitValueOutput(ctx, sink, "images", images.Images); err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	names := make([]string, 0, len(images.Images))
	for _, image := range images.Images {
		names = append(names, image.Name)
	}

	if err := sink.SetMultiline(ctx, "image-names", names); err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	return images, nil
}

// ResolveReleaseImagesPlan is the plan EmitReleaseImagesPlan emits, for a
// caller that hands it on as a file instead of as CI outputs.
func ResolveReleaseImagesPlan(in EmitReleaseImagesPlanInput, annot output.Annotator) (pipeline.ReleaseImagesPlan, error) {
	return resolveReleaseImagesPlan(in, annot)
}

func resolveReleaseImagesPlan(in EmitReleaseImagesPlanInput, annot output.Annotator) (pipeline.ReleaseImagesPlan, error) {
	plan, err := planReleaseImages(in, annot)
	if err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	if err := in.Runners.Admit(plan.RunnerNeeds()); err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	return plan, nil
}

func planReleaseImages(in EmitReleaseImagesPlanInput, annot output.Annotator) (pipeline.ReleaseImagesPlan, error) {
	if in.ResolvedPlan != "" {
		if len(in.Images.Labels) != 0 {
			return pipeline.ReleaseImagesPlan{}, fmt.Errorf("image labels are planned from artifacts.yml; a resolved image plan carries its own: %w", errs.ErrUsage)
		}

		plan, err := pipeline.ReadImageBuildPlan(in.ResolvedPlan, in.Images.ImageRepository, in.Images.ReleaseTag)
		if err != nil {
			return plan, err
		}

		if err := matchResolvedImagePlatforms(plan, in.Images.ReleasePlatforms); err != nil {
			return pipeline.ReleaseImagesPlan{}, err
		}

		return plan, nil
	}

	containers, err := releaseImageContainers(in, annot)
	if err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	return pipeline.PlanReleaseImages(containers, in.Images)
}

func matchResolvedImagePlatforms(plan pipeline.ReleaseImagesPlan, requested string) error {
	if requested == "" {
		return nil
	}

	for _, image := range plan.Images {
		var declared []string
		for arch := range strings.FieldsSeq(image.Archs) {
			declared = append(declared, "linux/"+arch)
		}

		selected, err := domaincontainer.SelectPlatforms(declared, requested)
		if err != nil {
			return err
		}

		if !slices.Equal(selected, declared) {
			return fmt.Errorf("resolved image plan must match the requested platforms: %w", errs.ErrInvalidConfig)
		}
	}

	return nil
}

// releaseImageContainers returns the declared containers, or the default
// image when no artifacts.yml exists and the caller allows it. Calling the
// release-images plan is the repository's choice to release images, so the
// default applies there only: the general config plan derives no container.
func releaseImageContainers(in EmitReleaseImagesPlanInput, annot output.Annotator) ([]pipeline.PlannedContainer, error) {
	if _, err := readArtifactsFile(in.Config); errors.Is(err, fs.ErrNotExist) {
		if !in.DefaultWhenAbsent {
			return nil, fmt.Errorf("%s does not exist: %w", in.Config.Path, errs.ErrMissingInput)
		}

		return defaultReleaseImage(in.Config.FS, in.Images.Repository, in.Config.Path, annot)
	}

	plan, err := loadConfigPlan(in.Config, annot)
	if err != nil {
		return nil, err
	}

	return plan.Containers.All, nil
}

// defaultReleaseImage is one container named after the repository, so its
// image is the repository's own, built from the root Containerfile.
func defaultReleaseImage(fsys fs.FS, source, path string, annot output.Annotator) ([]pipeline.PlannedContainer, error) {
	if !statable(fsys, config.DefaultContainerFile) {
		return nil, fmt.Errorf("no %s and no %s at the repository root: declare the images under containers: in artifacts.yml: %w", path, config.DefaultContainerFile, errs.ErrMissingInput)
	}

	parsed, err := repository.Parse(source)
	if err != nil {
		return nil, err
	}

	cfg := &config.Config{Containers: []config.Container{{Name: strings.ToLower(parsed.Name())}}}
	if err := config.Validate(cfg); err != nil {
		return nil, fmt.Errorf("the default image is named after the repository: %w; declare it in artifacts.yml", err)
	}

	if err := config.Derive(cfg); err != nil {
		return nil, err
	}

	annot.Noticef("No %s found; releasing the repository's image from its root %s.", path, config.DefaultContainerFile)

	return pipeline.NewConfigPlan(cfg).Containers.All, nil
}

// loadConfig reads + parses the artifacts.yml at in.Path. When the
// file is absent, it falls back to AutoDeriveConfig — the operator
// gets a working plan from a single root manifest without writing any
// configuration. annot.Noticef announces which path was taken so the
// behaviour is visible in CI logs.
func loadConfig(in EmitConfigPlanInput, annot output.Annotator) (*config.Config, error) {
	data, err := readArtifactsFile(in)
	if err == nil {
		cfg, parseErr := config.Parse(data)
		if parseErr != nil {
			// Parse classifies every failure as ErrInvalidConfig itself; a
			// second wrap doubled the "invalid configuration" tail.
			return nil, fmt.Errorf("parse %s: %w", in.Path, parseErr)
		}

		return cfg, nil
	}

	// File missing — try auto-derive from a single root manifest.
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", in.Path, err)
	}

	cfg, deriveErr := AutoDeriveConfig(in.FS, ".")
	if deriveErr != nil {
		return nil, deriveErr
	}

	annot.Noticef("No %s found; auto-derived a single-artifact plan from the root manifest.", in.Path)

	return cfg, nil
}

// readArtifactsFile reads the declaration. The plan it decides must be the
// file the repository holds, so a relative path, which is what every release
// workflow passes, is read inside the working directory: a symbolic link
// above it that leads out, or a declaration that is itself a link, is
// refused. An absolute path is a file the operator named outright, read at
// its own directory with the same refusal of a link.
func readArtifactsFile(in EmitConfigPlanInput) ([]byte, error) {
	if in.FS == nil {
		if in.Path == cliio.StdSentinel {
			return cliio.ReadFile(in.Path)
		}

		dir, name := ".", in.Path
		if filepath.IsAbs(in.Path) {
			dir, name = filepath.Split(in.Path)
		}

		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		defer func() { _ = root.Close() }()

		return cliio.ReadFileInRootNoFollow(root, name)
	}
	// An in-memory fs.FS doesn't carry stdin semantics; only honour
	// the "-" sentinel when reading from the real filesystem above.
	path := filepath.ToSlash(filepath.Clean(in.Path))
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, "/")

	if info, err := fs.Lstat(in.FS, path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%q is a symbolic link, not a regular input file: %w", in.Path, errs.ErrMissingInput)
	}

	return fs.ReadFile(in.FS, path)
}

func emitValueOutput[T any](ctx context.Context, sink ci.OutputSink, key string, value T) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return sink.Set(ctx, key, string(b))
}

func renderConfigSummary(plan pipeline.ConfigPlan) string {
	// Every value comes from artifacts.yml, which a pull request can edit, so
	// each is rendered as literal text rather than Markdown.
	text := domainsummary.LiteralText

	var builder strings.Builder
	builder.WriteString("## Configuration\n")

	for _, a := range plan.Artifacts.All {
		_, _ = fmt.Fprintf(&builder, "### %s\n", text(a.Name))
		_, _ = fmt.Fprintf(&builder, "- **Type:** %s\n", text(string(a.ProjectType)))
		_, _ = fmt.Fprintf(&builder, "- **Publish To:** %s\n", text(joinPublishTo(a.PublishTo)))
		_, _ = fmt.Fprintf(&builder, "- **Directory:** %s\n\n", text(a.WorkingDirectory))
	}

	if len(plan.Containers.All) > 0 {
		builder.WriteString("\n## Containers\n")

		for _, c := range plan.Containers.All {
			_, _ = fmt.Fprintf(&builder, "### %s\n", text(c.Name))
			_, _ = fmt.Fprintf(&builder, "- **From:** %s\n", text(strings.Join(c.From, ", ")))
			_, _ = fmt.Fprintf(&builder, "- **Artifact Types:** %s\n", text(joinProjectTypes(c.ArtifactTypes)))
			_, _ = fmt.Fprintf(&builder, "- **Containerfile:** %s\n\n", text(c.ContainerFile))
		}
	}

	return builder.String()
}

func joinPublishTo(targets []config.PublishTarget) string {
	parts := make([]string, len(targets))
	for i, t := range targets {
		parts[i] = string(t)
	}

	return strings.Join(parts, ", ")
}

func joinProjectTypes(types []projecttype.Type) string {
	parts := make([]string, len(types))
	for i, t := range types {
		parts[i] = string(t)
	}

	return strings.Join(parts, ", ")
}

// ReleaseStagePlans are the prepare and build stage plans of a release whose
// policy decides signing and publication, from its declaration alone.
type ReleaseStagePlans struct {
	Prepare pipeline.ReleasePrepareStagePlan
	Build   pipeline.ReleaseBuildStagePlan
	// DistroPackages is the declaration's resolved distro-packages block,
	// nil without one: each forge's plan admits or refuses it.
	DistroPackages *config.DistroPackages
	// Sign is the declaration's sign block as written, empty without one:
	// each forge's plan says how its release signs in its place.
	Sign config.SignConfig
}

// LoadReleaseStagePlans plans the version bump and the builds of the
// declaration at in.Path, as `plan release` does for the same declaration.
// Without the file, and with absentIsEmpty, the release has neither: a
// repository without a declaration builds with its build commands alone.
func LoadReleaseStagePlans(in EmitConfigPlanInput, annot output.Annotator, absentIsEmpty bool) (ReleaseStagePlans, error) {
	if _, err := readArtifactsFile(in); errors.Is(err, fs.ErrNotExist) {
		if !absentIsEmpty {
			return ReleaseStagePlans{}, fmt.Errorf("%s does not exist: %w", in.Path, errs.ErrMissingInput)
		}

		prepare, build := pipeline.UndeclaredReleaseStagePlans()

		return ReleaseStagePlans{Prepare: prepare, Build: build}, nil
	}

	cfg, err := loadPlannableConfig(in, annot)
	if err != nil {
		return ReleaseStagePlans{}, err
	}

	plan := pipeline.NewConfigPlan(cfg)

	prepare, build, err := pipeline.NewPolicyReleaseStagePlans(plan)
	if err != nil {
		return ReleaseStagePlans{}, err
	}

	return ReleaseStagePlans{Prepare: prepare, Build: build, DistroPackages: plan.DistroPackages, Sign: cfg.Sign}, nil
}
