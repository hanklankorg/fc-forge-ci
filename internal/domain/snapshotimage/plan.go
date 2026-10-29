// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package snapshotimage defines the provider-neutral snapshot image handoff.
package snapshotimage

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

const Version = 1

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
}

type Dependency struct {
	Name         string           `json:"name"`
	ArtifactName string           `json:"artifact_name"`
	Destination  string           `json:"destination"`
	ProjectType  projecttype.Type `json:"project_type"`
}

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

type Input struct {
	SourceSHA, SourceBranch, Tag, ImageRepository string
	SourceDateEpoch                               string
	Publish                                       bool
}

// New consumes the existing typed config plan, with snapshot-specific evidence
// policy already resolved by the application. No root Containerfile is inferred.
func New(cfg pipeline.ConfigPlan, in Input) (Plan, error) {
	p := Plan{Version: Version, Enabled: in.Publish && len(cfg.Containers.All) > 0, SourceSHA: in.SourceSHA, SourceBranch: in.SourceBranch, Tag: in.Tag, Builds: []Build{}, Images: []Image{}, Artifacts: []pipeline.PlannedArtifact{}}
	p.BaseVersion, _, _ = strings.Cut(in.Tag, "-")
	p.SourceDateEpoch = in.SourceDateEpoch
	if !git.ValidCommitSHA(in.SourceSHA) || strings.TrimSpace(in.SourceBranch) == "" {
		return p, fmt.Errorf("snapshot source requires an exact commit and logical branch")
	}
	if !p.Enabled {
		return p, p.Validate()
	}
	if !container.ValidRepositoryName(in.ImageRepository) {
		return p, fmt.Errorf("snapshot requires a full base OCI repository")
	}
	for _, c := range cfg.Containers.All {
		if len(c.BuildSecrets) > 0 || c.ExtractBinaryTarget != "" || len(c.ExtractBinaryNames) > 0 || len(c.ExtractBinaryPaths) > 0 || c.EnableSLSA {
			return p, fmt.Errorf("container %s: snapshot build secrets, extraction and OCI provenance are unsupported", c.Name)
		}
		repo := in.ImageRepository
		if c.ImageName != "" {
			registry, _, _ := strings.Cut(in.ImageRepository, "/")
			var err error
			repo, err = container.ResolveImageName(container.ResolveImageNameInput{Registry: registry, ImageName: c.ImageName})
			if err != nil {
				return p, err
			}
		}
		// Suffix is part of the base; the branch and commit retain their exact
		// postfix positions, and the version composer budgets arch tags again.
		tag := in.Tag
		if c.TagSuffix != "" {
			stem := strings.TrimSuffix(in.Tag, "-snapshot")
			index := strings.LastIndex(stem, "-")
			if index < 0 {
				return p, fmt.Errorf("invalid snapshot tag")
			}
			short := stem[index+1:]
			var err error
			tag, err = version.ComposeSnapshotContainerTag(p.BaseVersion+c.TagSuffix, in.SourceBranch, short)
			if err != nil {
				return p, err
			}
		}
		platforms := strings.FieldsFunc(c.Platforms, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
		severity := c.ScanSeverity
		if severity == "" {
			severity = "CRITICAL,HIGH"
		}
		im := Image{Name: c.Name, ImageRepository: repo, Tag: tag, TagSuffix: c.TagSuffix, Platforms: platforms, Scan: c.EnableScan, Severity: severity, SBOM: c.EnableAnalyzedContainerSBOM, Signing: "NONE"}
		deps, err := dependencies(cfg, c)
		if err != nil {
			return p, err
		}
		p.Images = append(p.Images, im)
		for _, platform := range platforms {
			p.Builds = append(p.Builds, Build{Name: c.Name, Arch: strings.TrimPrefix(platform, "linux/"), ImageRepository: repo, Tag: tag, ContainerFile: c.ContainerFile, Context: c.Context, Target: c.Target, BuildArgs: c.BuildArgs, RequiredArtifacts: deps})
		}
	}
	required := map[string]bool{}
	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			required[d.Name] = true
		}
	}
	for _, a := range cfg.Artifacts.All {
		if required[a.Name] {
			p.Artifacts = append(p.Artifacts, a)
		}
	}
	return p, p.Validate()
}

func dependencies(cfg pipeline.ConfigPlan, c pipeline.PlannedContainer) ([]Dependency, error) {
	out := []Dependency{}
	destinations := map[string]bool{}
	for _, name := range c.From {
		i := slices.IndexFunc(cfg.Artifacts.All, func(a pipeline.PlannedArtifact) bool { return a.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("container %s: missing dependency %s", c.Name, name)
		}
		a := cfg.Artifacts.All[i]
		if a.BuildArtifactName == "" {
			if (a.ProjectType == projecttype.Go && a.GoBuildMode == config.GoBuildModeContainerFirst) || (a.ProjectType == projecttype.Cargo && a.CargoBuildMode == config.CargoBuildModeContainerFirst) {
				continue
			}
			return nil, fmt.Errorf("container %s: dependency %s has no supported built artifact", c.Name, name)
		}
		dest := ""
		switch a.ProjectType {
		case projecttype.Go, projecttype.Cargo:
			dest = filepath.Join(c.Context, "dist")
		case projecttype.Maven:
			dest = filepath.Join(a.WorkingDirectory, "target")
		case projecttype.Gradle:
			dest = filepath.Join(a.WorkingDirectory, "build/libs")
		case projecttype.NPM:
			dest = ArtifactDestination(a, c.Context)
		default:
			return nil, fmt.Errorf("container %s: snapshot dependency type %s is unsupported", c.Name, a.ProjectType)
		}
		if destinations[dest] {
			return nil, fmt.Errorf("container %s: multiple artifact dependencies share %s; snapshot inputs must have distinct destinations", c.Name, dest)
		}
		destinations[dest] = true
		out = append(out, Dependency{Name: name, ArtifactName: a.BuildArtifactName, Destination: dest, ProjectType: a.ProjectType})
	}
	return out, nil
}

func plainName(s string) bool {
	return s != "" && s != "." && pathsafe.Relative(s) && filepath.Base(s) == s && !strings.ContainsAny(s, "\\\r\n")
}

// Validate admits files again at every verb, including exact matrix completeness.
func (p Plan) Validate() error {
	if p.Version != Version || !git.ValidCommitSHA(p.SourceSHA) || !git.ValidRefName("refs/heads/"+p.SourceBranch) {
		return fmt.Errorf("invalid snapshot plan identity")
	}
	if !p.Enabled {
		if len(p.Builds)+len(p.Images)+len(p.Artifacts) != 0 {
			return fmt.Errorf("disabled snapshot plan has work")
		}
		return nil
	}
	if len(p.Images) == 0 || !strings.HasSuffix(p.Tag, "-snapshot") {
		return fmt.Errorf("enabled snapshot plan has no images or postfix tag")
	}
	stem := strings.TrimSuffix(p.Tag, "-snapshot")
	index := strings.LastIndex(stem, "-")
	if index < 0 {
		return fmt.Errorf("snapshot tag omits source commit")
	}
	short := stem[index+1:]
	canonical, err := version.ComposeSnapshotContainerTag(p.BaseVersion, p.SourceBranch, short)
	if err != nil || !version.IsStableSemverTag("v"+p.BaseVersion) || canonical != p.Tag || !strings.HasPrefix(p.SourceSHA, short) {
		return fmt.Errorf("snapshot tag differs from logical branch or source commit")
	}
	pending := map[string]Image{}
	names := map[string]bool{}
	refs := map[string]bool{}
	for _, im := range p.Images {
		expectedTag, err := version.ComposeSnapshotContainerTag(p.BaseVersion+im.TagSuffix, p.SourceBranch, short)
		if err != nil || expectedTag != im.Tag {
			return fmt.Errorf("snapshot image %s tag differs from frozen base, suffix or branch", im.Name)
		}
		if err := config.Validate(&config.Config{Containers: []config.Container{{Name: im.Name, TagSuffix: im.TagSuffix}}}); err != nil {
			return err
		}
		if !plainName(im.Name) || names[im.Name] || !container.ValidRepositoryName(im.ImageRepository) || im.Signing != "NONE" || im.Provenance || len(im.Platforms) == 0 || !strings.HasSuffix(im.Tag, "-"+short+"-snapshot") {
			return fmt.Errorf("invalid or duplicate snapshot image %q", im.Name)
		}
		names[im.Name] = true
		if refs[im.ImageRepository+":"+im.Tag] {
			return fmt.Errorf("conflicting snapshot image destination")
		}
		refs[im.ImageRepository+":"+im.Tag] = true
		for _, platform := range im.Platforms {
			if platform != "linux/amd64" && platform != "linux/arm64" {
				return fmt.Errorf("snapshot requires native linux/amd64 or linux/arm64")
			}
			key := im.Name + "/" + strings.TrimPrefix(platform, "linux/")
			if _, exists := pending[key]; exists {
				return fmt.Errorf("duplicate snapshot platform")
			}
			pending[key] = im
		}
		for _, sev := range strings.Split(im.Severity, ",") {
			if !slices.Contains([]string{"UNKNOWN", "LOW", "MEDIUM", "HIGH", "CRITICAL"}, sev) {
				return fmt.Errorf("unsupported snapshot scan severity %q", sev)
			}
		}
		if err := container.ValidateManifestTags(im.ImageRepository, []string{im.ImageRepository + ":" + im.Tag, im.ImageRepository + ":" + im.Tag + "-amd64", im.ImageRepository + ":" + im.Tag + "-arm64"}); err != nil {
			return err
		}
	}
	for _, b := range p.Builds {
		key := b.Name + "/" + b.Arch
		im, ok := pending[key]
		if !ok || b.ImageRepository != im.ImageRepository || b.Tag != im.Tag || !pathsafe.Relative(b.Context) || !pathsafe.Relative(b.ContainerFile) {
			return fmt.Errorf("invalid or duplicate snapshot build %s", key)
		}
		for _, d := range b.RequiredArtifacts {
			if !plainName(d.Name) || !plainName(d.ArtifactName) || !pathsafe.Relative(d.Destination) || d.Destination == "." || filepath.ToSlash(filepath.Clean(d.Destination)) != d.Destination || strings.Contains(d.Destination, "\\") {
				return fmt.Errorf("invalid snapshot artifact dependency")
			}
			for _, part := range strings.Split(filepath.ToSlash(d.Destination), "/") {
				switch strings.ToLower(part) {
				case ".git", "snapshot-plan.json", "source-sha", "manifest.sha256", DefaultDependenciesFile:
					return fmt.Errorf("reserved snapshot artifact destination %q", d.Destination)
				}
			}
		}
		delete(pending, key)
	}
	if len(pending) > 0 {
		return fmt.Errorf("snapshot plan omits native builds")
	}
	if err := p.validateArtifacts(); err != nil {
		return err
	}
	_, err = p.artifactTransfers()
	return err
}

func (p Plan) Image(name string) (Image, error) {
	if err := p.Validate(); err != nil {
		return Image{}, err
	}
	for _, im := range p.Images {
		if im.Name == name {
			return im, nil
		}
	}
	return Image{}, fmt.Errorf("unplanned snapshot image %q", name)
}

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
func Collect(p Plan, receipts []Receipt) ([]Receipt, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if len(receipts) != len(p.Images) {
		return nil, fmt.Errorf("snapshot receipts must cover exactly all planned images")
	}
	byName := map[string]Receipt{}
	for _, r := range receipts {
		im, err := p.Image(r.Name)
		if err != nil {
			return nil, err
		}
		if _, ok := byName[r.Name]; ok {
			return nil, fmt.Errorf("duplicate snapshot receipt %s", r.Name)
		}
		if r.Version != Version || r.SourceSHA != p.SourceSHA || r.SourceBranch != p.SourceBranch || r.ImageRepository != im.ImageRepository || r.Tag != im.Tag || !container.ValidDigest(r.Digest) || r.Ref != im.ImageRepository+"@"+r.Digest || !slices.Equal(r.Platforms, im.Platforms) {
			return nil, fmt.Errorf("snapshot receipt identity differs from plan: %s", r.Name)
		}
		byName[r.Name] = r
	}
	out := make([]Receipt, 0, len(p.Images))
	for _, im := range p.Images {
		out = append(out, byName[im.Name])
	}
	return out, nil
}
