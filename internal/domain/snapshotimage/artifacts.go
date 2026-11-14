// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

var (
	errUnplannedArtifact       = errors.New("unplanned snapshot artifact")
	errUnfrozenSourceDateEpoch = errors.New("snapshot producers require frozen source_date_epoch")
	errForeignEcosystemConfig  = errors.New("has foreign ecosystem configuration")
	errInvalidProducer         = errors.New("invalid or duplicate snapshot producer")
	errDisagreesTypedConfig    = errors.New("disagrees with frozen typed configuration")
	errUnsupportedProducerType = errors.New("unsupported snapshot producer type")
	errDisagreesProducerConfig = errors.New("disagrees with frozen producer configuration")
	errMissingRequiredPlatform = errors.New("does not build required")
	errUnneededProducers       = errors.New("snapshot plan contains unneeded producers")
)

// DefaultProducerDir is the directory a snapshot producer writes its artifacts to.
const DefaultProducerDir = "_snapshot-artifacts"

// DefaultDownloadDir is the directory snapshot artifacts are downloaded into.
const DefaultDownloadDir = "_snapshot-downloads"

// ProducerManifest is the file name of a snapshot producer's artifact receipt.
const ProducerManifest = "snapshot-artifact.json"

// ArtifactDestination returns where a planned artifact's verified files are
// placed, or an empty string for unsupported types. Go, Cargo, Maven and
// Gradle land where their release builds leave the same files, so one
// Containerfile serves release and snapshot. An npm package is verified under
// an isolated directory of the context and, right before the build, laid over
// the context root, where the release image jobs lay the unpacked package.
func ArtifactDestination(a pipeline.PlannedArtifact, containerContext string) string {
	switch a.ProjectType {
	case projecttype.Go, projecttype.Cargo:
		return filepath.Join(containerContext, "dist")
	case projecttype.NPM:
		return filepath.Join(containerContext, ".forge-ci/snapshot-inputs", a.Name)
	case projecttype.Maven:
		return filepath.Join(a.WorkingDirectory, "target")
	case projecttype.Gradle:
		return filepath.Join(a.WorkingDirectory, "build/libs")
	default:
		return ""
	}
}

// Artifact validates the plan and returns the planned artifact with the given name.
func (p Plan) Artifact(name string) (pipeline.PlannedArtifact, error) {
	if err := p.Validate(); err != nil {
		return pipeline.PlannedArtifact{}, err
	}

	for _, a := range p.Artifacts {
		if a.Name == name {
			return a, nil
		}
	}

	return pipeline.PlannedArtifact{}, fmt.Errorf("%w %q", errUnplannedArtifact, name)
}

// ArtifactPlatforms returns the sorted platforms whose builds require the named artifact.
func (p Plan) ArtifactPlatforms(name string) []string {
	out := []string{}

	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			if d.Name == name && !slices.Contains(out, "linux/"+b.Arch) {
				out = append(out, "linux/"+b.Arch)
			}
		}
	}

	slices.Sort(out)

	return out
}

// requireSourceDateEpoch refuses a plan whose producers would build without
// the frozen source timestamp.
func (p Plan) requireSourceDateEpoch() error {
	epoch, err := strconv.ParseInt(p.SourceDateEpoch, 10, 64)
	if err != nil || epoch < 0 {
		return errUnfrozenSourceDateEpoch
	}

	return nil
}

func (p Plan) validateArtifacts() error {
	if len(p.Artifacts) > 0 {
		if err := p.requireSourceDateEpoch(); err != nil {
			return err
		}
	}

	byName := map[string]pipeline.PlannedArtifact{}

	for _, artifact := range p.Artifacts {
		if err := validateProducer(artifact, byName); err != nil {
			return err
		}

		byName[artifact.Name] = artifact
	}

	needed := map[string]bool{}

	for _, build := range p.Builds {
		if err := validateBuildDependencies(build, byName, needed); err != nil {
			return err
		}

		if err := requireOneNPMPackage(build.RequiredArtifacts); err != nil {
			return fmt.Errorf("snapshot build %s: %w", build.Name, err)
		}
	}

	if len(needed) != len(byName) {
		return errUnneededProducers
	}

	return nil
}

// validateProducer checks one planned producer against the frozen typed
// configuration it must have been derived from.
func validateProducer(artifact pipeline.PlannedArtifact, byName map[string]pipeline.PlannedArtifact) error {
	if err := validateEcosystemConfig(artifact); err != nil {
		return err
	}

	if !plainName(artifact.Name) || byName[artifact.Name].Name != "" || !pathsafe.Relative(artifact.WorkingDirectory) || filepath.ToSlash(filepath.Clean(artifact.WorkingDirectory)) != artifact.WorkingDirectory {
		return fmt.Errorf("%w %q", errInvalidProducer, artifact.Name)
	}

	cfgArtifact := config.Artifact{Name: artifact.Name, ProjectType: artifact.ProjectType, WorkingDirectory: artifact.WorkingDirectory, BuildType: artifact.BuildType, PublishTo: artifact.PublishTo, SBOMs: artifact.SBOMs, RequireAuthorization: artifact.RequireAuthorization, Go: artifact.Go, Cargo: artifact.Cargo, Maven: artifact.Maven, NPM: artifact.NPM, Gradle: artifact.Gradle}

	cfg := &config.Config{Artifacts: []config.Artifact{cfgArtifact}}
	if err := config.Validate(cfg); err != nil {
		return err
	}

	if err := config.Derive(cfg); err != nil {
		return err
	}

	expected := pipeline.NewConfigPlan(cfg).Artifacts.All[0]

	actualJSON, err := json.Marshal(artifact)
	if err != nil {
		return err
	}

	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return err
	}

	if artifact.BuildArtifactName == "" || !bytes.Equal(actualJSON, expectedJSON) {
		return fmt.Errorf("snapshot producer %s %w", artifact.Name, errDisagreesTypedConfig)
	}

	_, err = ToolchainFamily(artifact.ProjectType)

	return err
}

// validateEcosystemConfig rejects a producer carrying configuration for an
// ecosystem other than its own project type.
func validateEcosystemConfig(artifact pipeline.PlannedArtifact) error {
	configs := []struct {
		present bool
		kind    projecttype.Type
	}{{artifact.Go != nil, projecttype.Go}, {artifact.Cargo != nil, projecttype.Cargo}, {artifact.Maven != nil, projecttype.Maven}, {artifact.NPM != nil, projecttype.NPM}, {artifact.Gradle != nil, projecttype.Gradle}, {artifact.GradleAndroid != nil, projecttype.GradleAndroid}, {artifact.XcodeIOS != nil, projecttype.XcodeIOS}, {artifact.Python != nil, projecttype.Python}}
	for _, c := range configs {
		if c.present && c.kind != artifact.ProjectType {
			return fmt.Errorf("snapshot producer %s %w", artifact.Name, errForeignEcosystemConfig)
		}
	}

	return nil
}

// validateBuildDependencies checks a build's required artifacts against the
// admitted producers and records each one as needed.
func validateBuildDependencies(build Build, byName map[string]pipeline.PlannedArtifact, needed map[string]bool) error {
	for _, dep := range build.RequiredArtifacts {
		artifact, ok := byName[dep.Name]
		if !ok || dep.ArtifactName != artifact.BuildArtifactName || dep.ProjectType != artifact.ProjectType || dep.Destination != ArtifactDestination(artifact, build.Context) {
			return fmt.Errorf("snapshot dependency %s %w", dep.Name, errDisagreesProducerConfig)
		}

		needed[dep.Name] = true

		platforms := ""
		if artifact.Go != nil {
			platforms = artifact.Go.Platforms
		}

		if artifact.Cargo != nil {
			platforms = artifact.Cargo.Platforms
		}

		if platforms != "" && !slices.Contains(strings.FieldsFunc(platforms, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }), "linux/"+build.Arch) {
			return fmt.Errorf("snapshot producer %s %w linux/%s", artifact.Name, errMissingRequiredPlatform, build.Arch)
		}
	}

	return nil
}

// ArtifactVersion returns the snapshot version artifacts are built with, or an
// empty string when the tag carries no source commit.
func (p Plan) ArtifactVersion() string {
	stem := strings.TrimSuffix(p.Tag, "-snapshot")

	_, after, ok := strings.CutLast(stem, "-")
	if !ok {
		return ""
	}

	return version.ComposeSnapshotVersion(p.BaseVersion, p.SourceBranch, after)
}

// ArtifactReceipt records what a snapshot producer built for the plan.
type ArtifactReceipt struct {
	Version      int                      `json:"version"`
	SourceSHA    string                   `json:"source_sha"`
	SourceBranch string                   `json:"source_branch"`
	PlanSHA256   string                   `json:"plan_sha256"`
	Artifact     pipeline.PlannedArtifact `json:"artifact"`
	Platforms    []string                 `json:"platforms"`
	Files        []DependencyFile         `json:"files"`
}
