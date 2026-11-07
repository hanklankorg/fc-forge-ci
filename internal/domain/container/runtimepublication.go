// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	gitdomain "github.com/diggsweden/forge-ci/internal/domain/git"
)

// RuntimeImage is a product-owned build target and its declared native legs.
// The engine validates the declaration; it does not impose one product roster.
type RuntimeImage struct {
	Target string   `json:"target"`
	Name   string   `json:"name"`
	Archs  []string `json:"archs"`
}

// RuntimeBuildLeg identifies one unsigned native build.
type RuntimeBuildLeg struct {
	Target string `json:"target"`
	Name   string `json:"name"`
	Arch   string `json:"arch"`
}

// RuntimePublicationPlan binds native build and image matrices to a candidate tag.
type RuntimePublicationPlan struct {
	Builds       []RuntimeBuildLeg
	Images       []RuntimeImage
	CandidateTag string
}

// RuntimePublicationInput supplies product declarations and observed source identity.
type RuntimePublicationInput struct {
	Images                                          []RuntimeImage
	Architectures                                   []string
	Namespace, SourceSHA, CheckedOutSHA, RunID, Ref string
}

var (
	runtimeCoordinate = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	runtimeRunID      = regexp.MustCompile(`^[0-9]+-[0-9]+$`)
)

// PlanRuntimePublication admits the complete declaration before selecting any
// work. Unselected rows are validated too, so an invalid roster cannot hide
// behind a narrower architecture selection. Matrix order follows the product.
func PlanRuntimePublication(in RuntimePublicationInput) (RuntimePublicationPlan, error) {
	if in.Ref != "refs/heads/main" || !gitdomain.ValidCommitSHA(in.SourceSHA) || in.SourceSHA != in.CheckedOutSHA || !runtimeRunID.MatchString(in.RunID) {
		return RuntimePublicationPlan{}, fmt.Errorf("runtime publication requires main, an exact source checkout and a numeric run-attempt identity: %w", errs.ErrValidation)
	}

	if !ValidRepositoryName(in.Namespace+"/runtime") || len(in.Images) == 0 || len(in.Architectures) == 0 {
		return RuntimePublicationPlan{}, fmt.Errorf("runtime publication requires a registry namespace, roster and native platform selection: %w", errs.ErrInvalidConfig)
	}

	if err := validateRuntimeArchitectures(in.Architectures); err != nil {
		return RuntimePublicationPlan{}, err
	}

	if err := validateRuntimeRoster(in.Images); err != nil {
		return RuntimePublicationPlan{}, err
	}

	plan := RuntimePublicationPlan{CandidateTag: "staging-" + in.RunID + "-" + in.SourceSHA[:12]}
	for _, image := range in.Images {
		selected := RuntimeImage{Target: image.Target, Name: image.Name}
		for _, arch := range image.Archs {
			if slices.Contains(in.Architectures, arch) {
				selected.Archs = append(selected.Archs, arch)
				plan.Builds = append(plan.Builds, RuntimeBuildLeg{Target: image.Target, Name: image.Name, Arch: arch})
			}
		}

		if len(selected.Archs) > 0 {
			plan.Images = append(plan.Images, selected)
		}
	}

	if len(plan.Builds) == 0 {
		return RuntimePublicationPlan{}, fmt.Errorf("runtime platform selection produces no builds: %w", errs.ErrInvalidConfig)
	}

	return plan, nil
}

func validateRuntimeRoster(images []RuntimeImage) error {
	targets, names := map[string]bool{}, map[string]bool{}
	for _, image := range images {
		if !runtimeCoordinate.MatchString(image.Target) || !runtimeCoordinate.MatchString(image.Name) || targets[image.Target] || names[image.Name] {
			return fmt.Errorf("runtime targets and image names must be unique plain coordinates: %w", errs.ErrInvalidConfig)
		}

		if err := validateRuntimeArchitectures(image.Archs); err != nil {
			return err
		}

		targets[image.Target], names[image.Name] = true, true
	}

	return nil
}

func validateRuntimeArchitectures(archs []string) error {
	if len(archs) == 0 {
		return fmt.Errorf("runtime architecture list is empty: %w", errs.ErrInvalidConfig)
	}

	for index, arch := range archs {
		if (arch != ArchAMD64 && arch != ArchARM64) || slices.Contains(archs[:index], arch) {
			return fmt.Errorf("runtime architectures must be unique amd64/arm64 entries: %w", errs.ErrInvalidConfig)
		}
	}

	return nil
}
