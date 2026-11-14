// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// UnmarshalJSON preserves the declarative planner's scan-by-default policy for
// resolved input too: an omitted scan is a scan, and ReadImageBuildPlan
// refuses an explicit false. Keep strict field admission even though the
// outer decoder delegates this object.
func (image *ReleaseImage) UnmarshalJSON(body []byte) error {
	type wireImage ReleaseImage

	value := wireImage{Scan: true}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&value); err != nil {
		return err
	}

	*image = ReleaseImage(value)

	return nil
}

// ReadImageBuildPlan admits resolved consumer policy into the same native
// build/publication interface used by artifacts.yml. The consumer owns graph
// and content identities; the engine requires a complete, confined build set.
func ReadImageBuildPlan(body, expectedRepository, releaseTag string) (ReleaseImagesPlan, error) {
	var plan ReleaseImagesPlan

	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&plan); err != nil {
		return plan, fmt.Errorf("decode image build plan: %w: %w", err, errs.ErrInvalidConfig)
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return plan, fmt.Errorf("image build plan must contain exactly one JSON document: %w", errs.ErrInvalidConfig)
	}
	// Workflows and the assembly CLI consume one architecture per line.
	for idx := range plan.Images {
		if !plan.Images[idx].Scan {
			return ReleaseImagesPlan{}, fmt.Errorf("image build plan image %q: scan: false is not supported: every published image is scanned, and the administrator policy decides whether findings block; remove the field: %w", plan.Images[idx].Name, errs.ErrInvalidConfig)
		}

		plan.Images[idx].Archs = strings.Join(strings.Fields(plan.Images[idx].Archs), "\n")
		if parsed, ok := version.ParseSemverTag(releaseTag); ok && parsed.Prerelease != "" && plan.Images[idx].ImageKind != string(imageledger.ImageKindBase) {
			plan.Images[idx].MovingTagName = ""
		}
	}

	if err := validateImageBuildPlan(plan, expectedRepository, releaseTag); err != nil {
		return ReleaseImagesPlan{}, err
	}

	return plan, nil
}

type imagePlanIndex struct {
	images  map[string]ReleaseImage
	pending map[string]bool
	tags    map[string]bool
	sboms   map[string]bool
}

func validateImageBuildPlan(plan ReleaseImagesPlan, expectedRepository, releaseTag string) error {
	if !domaincontainer.ValidRepositoryName(expectedRepository) || len(plan.Images) == 0 || len(plan.Builds) == 0 {
		return fmt.Errorf("image build plan needs one explicit repository and nonempty images/builds: %w", errs.ErrInvalidConfig)
	}

	index := imagePlanIndex{images: map[string]ReleaseImage{}, pending: map[string]bool{}, tags: map[string]bool{}, sboms: map[string]bool{}}
	for _, image := range plan.Images {
		if err := index.addImage(image, expectedRepository, releaseTag); err != nil {
			return err
		}
	}

	for _, build := range plan.Builds {
		if err := index.claimBuild(build, expectedRepository); err != nil {
			return err
		}
	}

	if len(index.pending) != 0 {
		return fmt.Errorf("image build plan omits selected architecture builds: %w", errs.ErrInvalidConfig)
	}

	return nil
}

func (index *imagePlanIndex) addImage(image ReleaseImage, repository, releaseTag string) error {
	if err := config.Validate(&config.Config{Containers: []config.Container{{Name: image.Name}}}); err != nil {
		return err
	}

	if _, duplicate := index.images[image.Name]; duplicate || image.ImageRepository != repository {
		return fmt.Errorf("duplicate image name or foreign image repository: %w", errs.ErrInvalidConfig)
	}

	if image.CandidateTag != "staging-"+image.FinalTagName {
		return fmt.Errorf("image candidate tag must be staging-<final-tag>: %w", errs.ErrInvalidConfig)
	}

	if err := errors.Join(validateImageCachePolicy(image), validateImageLedgerPolicy(image, repository, releaseTag)); err != nil {
		return err
	}

	role := image.Role
	if role == "" {
		role = image.Name
	}

	sbom := imageledger.DefaultSBOMPath(imageledger.ImageKind(image.ImageKind), image.Flavor, role)
	if index.sboms[sbom] {
		return fmt.Errorf("image build plan has conflicting SBOM path %q: %w", sbom, errs.ErrInvalidConfig)
	}

	index.sboms[sbom] = true

	arches, err := index.reserveArchitectures(image)
	if err != nil {
		return err
	}

	if err := index.reserveTags(image, repository, arches); err != nil {
		return err
	}

	index.images[image.Name] = image

	return nil
}

func validateImageLedgerPolicy(image ReleaseImage, repository, releaseTag string) error {
	if (image.BaseInputID != "" && !domaincontainer.ValidSHA256Hex(image.BaseInputID)) ||
		(image.ImageKind == string(imageledger.ImageKindBase) && (image.Flavor == "" || image.BaseInputID == "")) {
		return fmt.Errorf("base image policy requires its flavor and SHA-256 input identity: %w", errs.ErrInvalidConfig)
	}

	entry := imageledger.Entry{
		Role: image.Name, ImageKind: imageledger.ImageKind(image.ImageKind), Flavor: image.Flavor,
		FinalTag: repository + ":" + image.FinalTagName, CandidateTag: repository + ":" + image.CandidateTag,
		BaseRef: image.BaseRef, BaseInputID: image.BaseInputID,
	}
	if image.Role != "" {
		entry.Role = image.Role
	}

	entry.SBOM = imageledger.DefaultSBOMPath(entry.ImageKind, entry.Flavor, entry.Role)

	if image.ProvenanceJSON != "" {
		if err := json.Unmarshal([]byte(image.ProvenanceJSON), &entry.Provenance); err != nil {
			return fmt.Errorf("invalid image provenance JSON: %w: %w", err, errs.ErrInvalidConfig)
		}
	}

	if image.MovingTagName != "" {
		entry.MovingTag = repository + ":" + image.MovingTagName
	}

	return entry.ValidateBeforeCapture(releaseTag)
}

func validateImageCachePolicy(image ReleaseImage) error {
	base := image.ImageKind == string(imageledger.ImageKindBase)
	if image.Revision != "" && (!base || !git.ValidCommitSHA(image.Revision)) {
		return fmt.Errorf("only base images may declare a full content revision: %w", errs.ErrInvalidConfig)
	}

	if image.CacheTagPrefix != "" && (!base || image.Revision == "" || image.RequiredLabels == "") {
		return fmt.Errorf("architecture cache requires a base content revision and required labels: %w", errs.ErrInvalidConfig)
	}

	return nil
}

func (index *imagePlanIndex) reserveArchitectures(image ReleaseImage) ([]string, error) {
	arches := strings.Fields(image.Archs)
	if len(arches) == 0 {
		return nil, fmt.Errorf("image %q has no architectures: %w", image.Name, errs.ErrInvalidConfig)
	}

	for _, arch := range arches {
		key := image.Name + "/" + arch
		if (arch != domaincontainer.ArchAMD64 && arch != domaincontainer.ArchARM64) || index.pending[key] {
			return nil, fmt.Errorf("image %q has unsupported or duplicate architecture %q: %w", image.Name, arch, errs.ErrInvalidConfig)
		}

		index.pending[key] = true
	}

	return arches, nil
}

func (index *imagePlanIndex) reserveTags(image ReleaseImage, repository string, arches []string) error {
	tags := make([]string, 0, 3+len(arches))

	tags = append(tags, image.FinalTagName, image.CandidateTag)
	if image.MovingTagName != "" {
		tags = append(tags, image.MovingTagName)
	}

	for _, arch := range arches {
		tags = append(tags, image.CandidateTag+"-"+arch)
		if image.CacheTagPrefix != "" {
			tags = append(tags, image.CacheTagPrefix+"-"+arch)
		}
	}

	refs := make([]string, 0, len(tags))
	for _, tag := range tags {
		if index.tags[tag] {
			return fmt.Errorf("image build plan has conflicting tag %q: %w", tag, errs.ErrInvalidConfig)
		}

		index.tags[tag] = true
		refs = append(refs, repository+":"+tag)
	}

	return domaincontainer.ValidateManifestTags(repository, refs)
}

func (index *imagePlanIndex) claimBuild(build ReleaseImageBuild, repository string) error {
	image, exists := index.images[build.Name]
	key := build.Name + "/" + build.Arch

	if !exists || !index.pending[key] || !matchesPlannedImageBuild(build, image, repository) {
		return fmt.Errorf("build %q does not match an unclaimed planned image architecture: %w", key, errs.ErrInvalidConfig)
	}

	if !filepath.IsLocal(build.ContainerFile) || !filepath.IsLocal(build.Context) {
		return fmt.Errorf("image build paths must remain inside the consumer checkout: %w", errs.ErrInvalidConfig)
	}

	if err := validateImageBuilderMaterial(build.BuilderMaterialID, image.CacheTagPrefix != ""); err != nil {
		return err
	}

	if _, err := ReleaseImageLabels(imagePlanLines(build.Labels)); err != nil {
		// This is malformed resolved configuration, not CLI flag syntax.
		return fmt.Errorf("image build plan labels for %q: %s: %w", key, err.Error(), errs.ErrInvalidConfig)
	}

	request := domaincontainer.BuildRequest{
		Context: build.Context, Containerfile: build.ContainerFile, Target: build.Target,
		Platform: build.Platform, BuildArgs: imagePlanLines(build.BuildArgs),
		Labels: append(imagePlanLines(build.Labels), imagePlanLines(image.RequiredLabels)...),
		Mode:   domaincontainer.BuildModeOCI, OutputDir: "layout",
	}
	if err := request.Validate(); err != nil {
		return err
	}

	if err := ValidateRegistryInputs(build); err != nil {
		return err
	}

	delete(index.pending, key)

	return nil
}

// ValidateRegistryInputs binds every private parent to an existing image build
// argument. Fetching never evaluates a Containerfile or discovers dependencies
// from consumer code while credentials are present.
func ValidateRegistryInputs(build ReleaseImageBuild) error {
	arguments := map[string]string{}

	for _, argument := range imagePlanLines(build.BuildArgs) {
		key, value, _ := strings.Cut(argument, "=")
		arguments[key] = value
	}

	for key, ref := range build.RegistryInputs {
		if key == "" || key == "cache" || strings.ContainsAny(key, "/\\.\n\r=") || arguments[key] != ref {
			return fmt.Errorf("registry input must match a named image build argument: %w", errs.ErrInvalidConfig)
		}

		if !validRegistryInputArgument(key) {
			return fmt.Errorf("registry input argument name is invalid: %w", errs.ErrInvalidConfig)
		}

		repository, digest, ok := strings.Cut(ref, "@")
		if !ok || !domaincontainer.ValidRepositoryName(repository) || !domaincontainer.ValidDigest(digest) {
			return fmt.Errorf("registry input must name a digest-pinned parent image: %w", errs.ErrInvalidConfig)
		}
	}

	return nil
}

func validRegistryInputArgument(key string) bool {
	for _, character := range key {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' || character == '_' {
			continue
		}

		return false
	}

	return true
}

func validateImageBuilderMaterial(id string, cache bool) error {
	if id != "" && !domaincontainer.ValidSHA256Hex(id) {
		return fmt.Errorf("expected builder material ID must be a SHA-256 value: %w", errs.ErrInvalidConfig)
	}

	if cache && id == "" {
		return fmt.Errorf("architecture cache requires a native builder material ID: %w", errs.ErrInvalidConfig)
	}

	return nil
}

func matchesPlannedImageBuild(build ReleaseImageBuild, image ReleaseImage, repository string) bool {
	cacheTag := ""
	if image.CacheTagPrefix != "" {
		cacheTag = image.CacheTagPrefix + "-" + build.Arch
	}

	return build.Platform == "linux/"+build.Arch && build.ImageRepository == repository &&
		build.CandidateTag == image.CandidateTag && build.FinalTagName == image.FinalTagName &&
		build.Revision == image.Revision && build.CacheTag == cacheTag &&
		(cacheTag == "" || build.RequiredLabels == image.RequiredLabels)
}

func imagePlanLines(value string) []string {
	var lines []string

	for line := range strings.SplitSeq(value, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}

	return lines
}
