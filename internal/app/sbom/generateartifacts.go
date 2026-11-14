// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// GenerateArtifactsInput drives `sbom assemble --plan`.
type GenerateArtifactsInput struct {
	ConfigPlanJSON string
	SBOMs          string
	Version        string
	WorkingDir     string
	// AcquisitionDir selects the isolated producer tree in release fan-in.
	// Source working directories are metadata, not producer-file inventories.
	AcquisitionDir string
}

type sbomArtifact struct {
	Name              string
	Type              projecttype.Type
	WorkingDir        string
	EffectiveSBOMs    []config.SBOMLayer
	HasEffectiveSBOMs bool
}

// GenerateArtifacts expands release-level SBOM policy and generates artifact-level SBOMs per artifact.
func GenerateArtifacts(
	ctx context.Context,
	syft SyftOps,
	mvn MavenOps,
	gitRepo GitOps,
	out, stderr io.Writer,
	in GenerateArtifactsInput,
) error {
	layers, err := artifactLayers(in.SBOMs)
	if err != nil {
		return err
	}

	if err := validateSBOMAcquisition(in.AcquisitionDir); err != nil {
		return err
	}

	if len(layers) == 0 {
		_, _ = fmt.Fprintln(out, "No artifact-level SBOM layers requested; analyzed-container (if any) is already downloaded.")

		return nil
	}

	artifacts, err := sbomArtifacts(in)
	if err != nil {
		return err
	}

	if in.AcquisitionDir != "" && strings.TrimSpace(in.Version) == "" {
		return fmt.Errorf("version is required with acquisition-dir: %w", errs.ErrUsage)
	}

	generatedOutputs := make(map[string]bool)

	for _, artifact := range artifacts {
		if artifact.Type == projecttype.Meta {
			continue
		}

		artifactLayers := layers
		if artifact.HasEffectiveSBOMs {
			artifactLayers = intersectSBOMLayers(layers, artifact.EffectiveSBOMs)
		}

		if len(artifactLayers) == 0 {
			continue
		}

		workingDir := artifact.WorkingDir
		if workingDir == "" {
			workingDir = in.WorkingDir
		}

		if in.AcquisitionDir != "" {
			workingDir = in.AcquisitionDir
		}

		if err := Generate(ctx, syft, mvn, gitRepo, nil, out, stderr, GenerateInput{
			ProjectType:         string(artifact.Type),
			Layers:              sbomLayerCSV(artifactLayers),
			Version:             in.Version,
			Name:                artifact.Name,
			WorkingDir:          workingDir,
			RequireFreshOutputs: in.AcquisitionDir != "",
			generatedOutputs:    generatedOutputs,
		}); err != nil {
			return err
		}
	}

	return nil
}

func validateSBOMAcquisition(dir string) error {
	if dir == "" {
		return nil
	}

	if !filepath.IsAbs(dir) {
		return fmt.Errorf("acquisition-dir must be absolute: %w", errs.ErrValidation)
	}

	disjoint, err := pathsafe.DisjointRoots(".", dir)
	if err != nil {
		return err
	}

	if !disjoint {
		return fmt.Errorf("acquisition directory must be disjoint from the source checkout: %w", errs.ErrValidation)
	}

	return nil
}

func artifactLayers(sboms string) ([]config.SBOMLayer, error) {
	layers, err := config.ExpandSBOMs(sboms)
	if err != nil {
		return nil, err
	}

	out := make([]config.SBOMLayer, 0, len(layers))
	for _, layer := range layers {
		if layer == config.SBOMLayerAnalyzedContainer {
			continue
		}

		out = append(out, layer)
	}

	return out, nil
}

func sbomLayerCSV(layers []config.SBOMLayer) string {
	parts := make([]string, 0, len(layers))
	for _, layer := range layers {
		parts = append(parts, string(layer))
	}

	return strings.Join(parts, ",")
}

func intersectSBOMLayers(releaseLayers, artifactLayers []config.SBOMLayer) []config.SBOMLayer {
	allowed := make(map[config.SBOMLayer]bool, len(artifactLayers))
	for _, layer := range artifactLayers {
		allowed[layer] = true
	}

	out := make([]config.SBOMLayer, 0, len(releaseLayers))
	for _, layer := range releaseLayers {
		if allowed[layer] {
			out = append(out, layer)
		}
	}

	return out
}

func sbomArtifacts(in GenerateArtifactsInput) ([]sbomArtifact, error) {
	if strings.TrimSpace(in.ConfigPlanJSON) == "" {
		return nil, fmt.Errorf("config-plan-json is required: %w", errs.ErrUsage)
	}

	return parseConfigPlanSBOMArtifacts(in.ConfigPlanJSON)
}

func parseConfigPlanSBOMArtifacts(value string) ([]sbomArtifact, error) {
	plan, err := pipeline.DecodeConfigPlan(value)
	if err != nil {
		return nil, err
	}

	if plan.Version != pipeline.ConfigPlanVersion {
		return nil, fmt.Errorf("config-plan-json has unsupported version %d: %w", plan.Version, errs.ErrInvalidConfig)
	}

	if err := pipeline.ValidateConfigPlan(plan); err != nil {
		return nil, err
	}

	out := make([]sbomArtifact, 0, len(plan.Artifacts.All))
	for _, item := range plan.Artifacts.All {
		out = append(out, sbomArtifact{
			Name:              item.Name,
			Type:              item.ProjectType,
			WorkingDir:        item.WorkingDirectory,
			EffectiveSBOMs:    slices.Clone(item.EffectiveSBOMs),
			HasEffectiveSBOMs: true,
		})
	}

	return out, nil
}
