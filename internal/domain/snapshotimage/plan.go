// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package snapshotimage defines the provider-neutral snapshot image handoff.
package snapshotimage

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
	"github.com/diggsweden/forge-ci/internal/domain/security"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

var (
	errSourceIdentity          = errors.New("snapshot source requires an exact commit and logical branch")
	errBaseRepository          = errors.New("snapshot requires a full base OCI repository")
	errUnsupportedContainer    = errors.New("declares build-secrets, and a snapshot builds branch code without secrets; set publish-containers to false for this snapshot, or snapshot a declaration without this container")
	errInvalidTag              = errors.New("invalid snapshot tag")
	errMissingDependency       = errors.New("missing dependency")
	errNoBuiltArtifact         = errors.New("has no supported built artifact")
	errUnsupportedDependency   = errors.New("is unsupported")
	errSharedDestination       = errors.New("snapshot inputs must have distinct destinations")
	errNPMPackagesShareContext = errors.New("would both be laid over the build context root; an image takes one npm package")
	errPlanIdentity            = errors.New("invalid snapshot plan identity")
	errDisabledPlanHasWork     = errors.New("disabled snapshot plan has work")
	errEnabledPlanEmpty        = errors.New("enabled snapshot plan has no images or postfix tag")
	errTagOmitsCommit          = errors.New("snapshot tag omits source commit")
	errTagDiffers              = errors.New("snapshot tag differs from logical branch or source commit")
	errImageTagDiffers         = errors.New("tag differs from frozen base, suffix or branch")
	errInvalidImage            = errors.New("invalid or duplicate snapshot image")
	errConflictingDestination  = errors.New("conflicting snapshot image destination")
	errNonNativePlatform       = errors.New("snapshot requires native linux/amd64 or linux/arm64")
	errDuplicatePlatform       = errors.New("duplicate snapshot platform")
	errUnsupportedSeverity     = errors.New("unsupported snapshot scan severity")
	errUnscannedImage          = errors.New("snapshot plan publishes an image without its scan")
	errInvalidBuild            = errors.New("invalid or duplicate snapshot build")
	errInvalidDependency       = errors.New("invalid snapshot artifact dependency")
	errReservedDestination     = errors.New("reserved snapshot artifact destination")
	errMissingNativeBuilds     = errors.New("snapshot plan omits native builds")
	errUnplannedImage          = errors.New("unplanned snapshot image")
	errReceiptCount            = errors.New("snapshot receipts must cover exactly all planned images")
	errDuplicateReceipt        = errors.New("duplicate snapshot receipt")
	errReceiptIdentityDiffers  = errors.New("snapshot receipt identity differs from plan")
)

// Version is the snapshot plan and receipt schema version.
const Version = 1

// Plan is the frozen snapshot handoff: the images, native builds and artifact
// producers one snapshot run publishes, and the file outputs it keeps.
// Enabled reports whether images are published; Files are planned on their
// own, so a project without a container can still have file outputs.
type Plan struct {
	Version         int                        `json:"version"`
	Enabled         bool                       `json:"enabled"`
	SourceSHA       string                     `json:"source_sha"`
	SourceBranch    string                     `json:"source_branch"`
	BaseVersion     string                     `json:"base_version"`
	SourceDateEpoch string                     `json:"source_date_epoch"`
	Artifacts       []pipeline.PlannedArtifact `json:"artifacts"`
	Tag             string                     `json:"tag"`
	Builds          []Build                    `json:"builds"`
	Images          []Image                    `json:"images"`
	// Files are the artifacts whose built files the snapshot keeps as run
	// artifacts, each built on its own with every declared platform.
	Files []pipeline.PlannedArtifact `json:"files,omitempty"`
}

// Dependency is a built artifact a native build needs at a given destination.
type Dependency struct {
	Name         string           `json:"name"`
	ArtifactName string           `json:"artifact_name"`
	Destination  string           `json:"destination"`
	ProjectType  projecttype.Type `json:"project_type"`
}

// RunnerNeeds is the image build runner each planned build runs on.
func (plan Plan) RunnerNeeds() []runners.Need {
	needs := make([]runners.Need, 0, len(plan.Builds))

	for _, build := range plan.Builds {
		needs = append(needs, runners.Need{Role: runners.Build, Platform: "linux/" + build.Arch, Jobs: "image builds"})
	}

	return needs
}

// Build is one native per-architecture image build.
type Build struct {
	Name              string            `json:"name"`
	Arch              string            `json:"arch"`
	ImageRepository   string            `json:"image_repository"`
	Tag               string            `json:"tag"`
	ContainerFile     string            `json:"container_file"`
	Context           string            `json:"context"`
	Target            string            `json:"target"`
	BuildArgs         map[string]string `json:"build_args"`
	RequiredArtifacts []Dependency      `json:"required_artifacts"`
}

// Image is one published snapshot image and its evidence policy. Scan is
// always true: every published image is scanned, and the administrator policy
// alone decides whether findings block. The field stays in the frozen plan.
type Image struct {
	Name            string   `json:"name"`
	ImageRepository string   `json:"image_repository"`
	Tag             string   `json:"tag"`
	TagSuffix       string   `json:"tag_suffix"`
	Platforms       []string `json:"platforms"`
	Scan            bool     `json:"scan"`
	Severity        string   `json:"severity"`
	SBOM            bool     `json:"sbom"`
	Provenance      bool     `json:"provenance"`
	Signing         string   `json:"signing"`
}

// Input carries the run identity a snapshot plan is built for.
type Input struct {
	SourceSHA, SourceBranch, Tag, ImageRepository string
	SourceDateEpoch                               string
	// Publish enables the declared images, PublishFiles the file outputs.
	Publish, PublishFiles bool
}

// New consumes the existing typed config plan, with snapshot-specific evidence
// policy already resolved by the application. No root Containerfile is inferred.
func New(cfg pipeline.ConfigPlan, in Input) (Plan, error) {
	plan := Plan{Version: Version, Enabled: in.Publish && len(cfg.Containers.All) > 0, SourceSHA: in.SourceSHA, SourceBranch: in.SourceBranch, Tag: in.Tag, Builds: []Build{}, Images: []Image{}, Artifacts: []pipeline.PlannedArtifact{}}
	plan.BaseVersion, _, _ = strings.Cut(in.Tag, "-")

	plan.SourceDateEpoch = in.SourceDateEpoch
	if !git.ValidCommitSHA(in.SourceSHA) || strings.TrimSpace(in.SourceBranch) == "" {
		return plan, errSourceIdentity
	}

	if in.PublishFiles {
		files, err := fileOutputs(cfg)
		if err != nil {
			return plan, err
		}

		if len(files) > 0 {
			plan.Files = files
		}
	}

	if !plan.Enabled {
		return plan, plan.Validate()
	}

	if !container.ValidRepositoryName(in.ImageRepository) {
		return plan, errBaseRepository
	}

	for _, ctr := range cfg.Containers.All {
		image, builds, err := planContainer(cfg, in, plan.BaseVersion, ctr)
		if err != nil {
			return plan, err
		}

		plan.Images = append(plan.Images, image)
		plan.Builds = append(plan.Builds, builds...)
	}

	required := map[string]bool{}

	for _, b := range plan.Builds {
		for _, d := range b.RequiredArtifacts {
			required[d.Name] = true
		}
	}

	for _, a := range cfg.Artifacts.All {
		if required[a.Name] {
			plan.Artifacts = append(plan.Artifacts, a)
		}
	}

	return plan, plan.Validate()
}

// planContainer resolves one configured container into its snapshot image and
// one native build per platform. A declared binary extraction is a release's
// output, its binaries a release's assets: a snapshot publishes the runtime
// image alone, so it builds the container and extracts nothing.
func planContainer(cfg pipeline.ConfigPlan, in Input, baseVersion string, ctr pipeline.PlannedContainer) (Image, []Build, error) {
	if len(ctr.BuildSecrets) > 0 {
		return Image{}, nil, fmt.Errorf("container %s: %w", ctr.Name, errUnsupportedContainer)
	}

	repo, err := containerRepository(in.ImageRepository, ctr.ImageName)
	if err != nil {
		return Image{}, nil, err
	}

	tag, err := containerTag(in, baseVersion, ctr.TagSuffix)
	if err != nil {
		return Image{}, nil, err
	}

	platforms := strings.FieldsFunc(ctr.Platforms, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })

	// The plan records the normalised blocking threshold, the one the scan
	// applies.
	gate, err := security.ParseContainerSeverityGate(ctr.ScanSeverity)
	if err != nil {
		return Image{}, nil, fmt.Errorf("container %s: %w: %w", ctr.Name, errUnsupportedSeverity, err)
	}

	image := Image{Name: ctr.Name, ImageRepository: repo, Tag: tag, TagSuffix: ctr.TagSuffix, Platforms: platforms, Scan: true, Severity: gate.Threshold(), SBOM: ctr.EnableAnalyzedContainerSBOM, Signing: "NONE"}

	deps, err := dependencies(cfg, ctr)
	if err != nil {
		return Image{}, nil, err
	}

	builds := make([]Build, 0, len(platforms))
	for _, platform := range platforms {
		builds = append(builds, Build{Name: ctr.Name, Arch: strings.TrimPrefix(platform, "linux/"), ImageRepository: repo, Tag: tag, ContainerFile: ctr.ContainerFile, Context: ctr.Context, Target: ctr.Target, BuildArgs: ctr.BuildArgs, RequiredArtifacts: deps})
	}

	return image, builds, nil
}

// containerRepository resolves a container's image name against the base
// repository's registry; an empty name keeps the base repository.
func containerRepository(baseRepository, imageName string) (string, error) {
	if imageName == "" {
		return baseRepository, nil
	}

	registry, _, _ := strings.Cut(baseRepository, "/")

	return container.ResolveImageName(container.ResolveImageNameInput{Registry: registry, ImageName: imageName})
}

// containerTag composes a container's snapshot tag. Suffix is part of the base;
// the branch and commit retain their exact postfix positions, and the version
// composer budgets arch tags again.
func containerTag(in Input, baseVersion, tagSuffix string) (string, error) {
	if tagSuffix == "" {
		return in.Tag, nil
	}

	stem := strings.TrimSuffix(in.Tag, "-snapshot")

	_, after, ok := strings.CutLast(stem, "-")
	if !ok {
		return "", errInvalidTag
	}

	short := after

	return version.ComposeSnapshotContainerTag(baseVersion+tagSuffix, in.SourceBranch, short)
}

func dependencies(cfg pipeline.ConfigPlan, ctr pipeline.PlannedContainer) ([]Dependency, error) {
	out := []Dependency{}
	destinations := map[string]bool{}

	for _, name := range ctr.From {
		i := slices.IndexFunc(cfg.Artifacts.All, func(a pipeline.PlannedArtifact) bool { return a.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("container %s: %w %s", ctr.Name, errMissingDependency, name)
		}

		artifact := cfg.Artifacts.All[i]
		if artifact.BuildArtifactName == "" {
			if (artifact.ProjectType == projecttype.Go && artifact.GoBuildMode == config.GoBuildModeContainerFirst) || (artifact.ProjectType == projecttype.Cargo && artifact.CargoBuildMode == config.CargoBuildModeContainerFirst) {
				continue
			}

			return nil, fmt.Errorf("container %s: dependency %s %w", ctr.Name, name, errNoBuiltArtifact)
		}

		dest := ArtifactDestination(artifact, ctr.Context)
		if dest == "" {
			return nil, fmt.Errorf("container %s: snapshot dependency type %s %w", ctr.Name, artifact.ProjectType, errUnsupportedDependency)
		}

		if destinations[dest] {
			return nil, fmt.Errorf("container %s: multiple artifact dependencies share %s; %w", ctr.Name, dest, errSharedDestination)
		}

		destinations[dest] = true
		out = append(out, Dependency{Name: name, ArtifactName: artifact.BuildArtifactName, Destination: dest, ProjectType: artifact.ProjectType})
	}

	if err := requireOneNPMPackage(out); err != nil {
		return nil, fmt.Errorf("container %s: %w", ctr.Name, err)
	}

	return out, nil
}

// requireOneNPMPackage refuses two npm packages in one image: the build lays
// each verified package over the context root, and every package carries a
// package.json, so two would overwrite each other there.
func requireOneNPMPackage(deps []Dependency) error {
	names := []string{}

	for _, dep := range deps {
		if dep.ProjectType == projecttype.NPM {
			names = append(names, dep.Name)
		}
	}

	if len(names) > 1 {
		return fmt.Errorf("npm packages %s %w", strings.Join(names, " and "), errNPMPackagesShareContext)
	}

	return nil
}

func plainName(s string) bool {
	return s != "" && s != "." && pathsafe.Relative(s) && filepath.Base(s) == s && !strings.ContainsAny(s, "\\\r\n")
}

// Validate admits files again at every verb, including exact matrix completeness.
func (p Plan) Validate() error {
	if p.Version != Version || !git.ValidCommitSHA(p.SourceSHA) || !git.ValidRefName("refs/heads/"+p.SourceBranch) {
		return errPlanIdentity
	}

	if err := p.validateFiles(); err != nil {
		return err
	}

	if !p.Enabled {
		if len(p.Builds)+len(p.Images)+len(p.Artifacts) != 0 {
			return errDisabledPlanHasWork
		}

		return nil
	}

	short, err := p.validateTag()
	if err != nil {
		return err
	}

	pending, err := p.validateImages(short)
	if err != nil {
		return err
	}

	for _, build := range p.Builds {
		key := build.Name + "/" + build.Arch
		if err := validateBuild(build, key, pending); err != nil {
			return err
		}

		delete(pending, key)
	}

	if len(pending) > 0 {
		return errMissingNativeBuilds
	}

	if err := p.validateArtifacts(); err != nil {
		return err
	}

	_, err = p.artifactTransfers()

	return err
}

// validateBuild checks one native build against its pending image and its
// required artifact destinations.
func validateBuild(build Build, key string, pending map[string]Image) error {
	image, ok := pending[key]
	if !ok || build.ImageRepository != image.ImageRepository || build.Tag != image.Tag || !pathsafe.Relative(build.Context) || !pathsafe.Relative(build.ContainerFile) {
		return fmt.Errorf("%w %s", errInvalidBuild, key)
	}

	for _, dep := range build.RequiredArtifacts {
		if !plainName(dep.Name) || !plainName(dep.ArtifactName) || !pathsafe.Relative(dep.Destination) || dep.Destination == "." || filepath.ToSlash(filepath.Clean(dep.Destination)) != dep.Destination || strings.Contains(dep.Destination, "\\") {
			return errInvalidDependency
		}

		for part := range strings.SplitSeq(filepath.ToSlash(dep.Destination), "/") {
			switch strings.ToLower(part) {
			case ".git", "snapshot-plan.json", "source-sha", "manifest.sha256", DefaultDependenciesFile:
				return fmt.Errorf("%w %q", errReservedDestination, dep.Destination)
			}
		}
	}

	return nil
}

// Image validates the plan and returns the planned image with the given name.
func (p Plan) Image(name string) (Image, error) {
	if err := p.Validate(); err != nil {
		return Image{}, err
	}

	for _, im := range p.Images {
		if im.Name == name {
			return im, nil
		}
	}

	return Image{}, fmt.Errorf("%w %q", errUnplannedImage, name)
}

// validateTag checks the enabled plan's postfix tag against its frozen
// identity and returns the short source commit it carries.
func (p Plan) validateTag() (string, error) {
	if len(p.Images) == 0 || !strings.HasSuffix(p.Tag, "-snapshot") {
		return "", errEnabledPlanEmpty
	}

	return p.tagCommit()
}

// tagCommit checks the postfix tag against the plan's frozen base version,
// branch and source, and returns the short source commit it carries. Images
// are tagged with it and file outputs are named from it.
func (p Plan) tagCommit() (string, error) {
	stem, ok := strings.CutSuffix(p.Tag, "-snapshot")
	if !ok {
		return "", errTagDiffers
	}

	_, after, ok := strings.CutLast(stem, "-")
	if !ok {
		return "", errTagOmitsCommit
	}

	short := after

	canonical, err := version.ComposeSnapshotContainerTag(p.BaseVersion, p.SourceBranch, short)
	if err != nil || !version.IsStableSemverTag("v"+p.BaseVersion) || canonical != p.Tag || !strings.HasPrefix(p.SourceSHA, short) {
		return "", errTagDiffers
	}

	return short, nil
}

// validateImages admits every planned image and returns the native builds the
// images still expect, keyed by image name and architecture.
func (p Plan) validateImages(short string) (map[string]Image, error) {
	pending := map[string]Image{}
	names := map[string]bool{}
	refs := map[string]bool{}

	for _, image := range p.Images {
		if err := p.validateImage(image, short, names); err != nil {
			return nil, err
		}

		names[image.Name] = true
		if refs[image.ImageRepository+":"+image.Tag] {
			return nil, errConflictingDestination
		}

		refs[image.ImageRepository+":"+image.Tag] = true
		for _, platform := range image.Platforms {
			if platform != "linux/amd64" && platform != "linux/arm64" {
				return nil, errNonNativePlatform
			}

			key := image.Name + "/" + strings.TrimPrefix(platform, "linux/")
			if _, exists := pending[key]; exists {
				return nil, errDuplicatePlatform
			}

			pending[key] = image
		}

		// A plan always records its threshold; an empty one was not planned.
		if _, err := security.ParseContainerSeverityGate(image.Severity); image.Severity == "" || err != nil {
			return nil, fmt.Errorf("%w %q", errUnsupportedSeverity, image.Severity)
		}

		if !image.Scan {
			return nil, errUnscannedImage
		}

		if err := container.ValidateManifestTags(image.ImageRepository, []string{image.ImageRepository + ":" + image.Tag, image.ImageRepository + ":" + image.Tag + "-amd64", image.ImageRepository + ":" + image.Tag + "-arm64"}); err != nil {
			return nil, err
		}
	}

	return pending, nil
}

// validateImage checks one image's frozen tag, name and evidence policy.
func (p Plan) validateImage(image Image, short string, names map[string]bool) error {
	expectedTag, err := version.ComposeSnapshotContainerTag(p.BaseVersion+image.TagSuffix, p.SourceBranch, short)
	if err != nil || expectedTag != image.Tag {
		return fmt.Errorf("snapshot image %s %w", image.Name, errImageTagDiffers)
	}

	if err := config.Validate(&config.Config{Containers: []config.Container{{Name: image.Name, TagSuffix: image.TagSuffix}}}); err != nil {
		return err
	}

	if !plainName(image.Name) || names[image.Name] || !container.ValidRepositoryName(image.ImageRepository) || image.Signing != "NONE" || image.Provenance || len(image.Platforms) == 0 || !strings.HasSuffix(image.Tag, "-"+short+"-snapshot") {
		return fmt.Errorf("%w %q", errInvalidImage, image.Name)
	}

	return nil
}

// Receipt records one published snapshot image by digest.
type Receipt struct {
	Version         int      `json:"version"`
	SourceSHA       string   `json:"source_sha"`
	SourceBranch    string   `json:"source_branch"`
	Name            string   `json:"name"`
	ImageRepository string   `json:"image_repository"`
	Tag             string   `json:"tag"`
	Digest          string   `json:"digest"`
	Ref             string   `json:"ref"`
	Platforms       []string `json:"platforms"`
}

// Collect requires exactly the planned publication set, in plan order.
func Collect(plan Plan, receipts []Receipt) ([]Receipt, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}

	if len(receipts) != len(plan.Images) {
		return nil, errReceiptCount
	}

	byName := map[string]Receipt{}

	for _, receipt := range receipts {
		im, err := plan.Image(receipt.Name)
		if err != nil {
			return nil, err
		}

		if _, ok := byName[receipt.Name]; ok {
			return nil, fmt.Errorf("%w %s", errDuplicateReceipt, receipt.Name)
		}

		if receipt.Version != Version || receipt.SourceSHA != plan.SourceSHA || receipt.SourceBranch != plan.SourceBranch || receipt.ImageRepository != im.ImageRepository || receipt.Tag != im.Tag || !container.ValidDigest(receipt.Digest) || receipt.Ref != im.ImageRepository+"@"+receipt.Digest || !slices.Equal(receipt.Platforms, im.Platforms) {
			return nil, fmt.Errorf("%w: %s", errReceiptIdentityDiffers, receipt.Name)
		}

		byName[receipt.Name] = receipt
	}

	out := make([]Receipt, 0, len(plan.Images))
	for _, im := range plan.Images {
		out = append(out, byName[im.Name])
	}

	return out, nil
}
