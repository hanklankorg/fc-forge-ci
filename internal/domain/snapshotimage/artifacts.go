// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"bytes"
	"encoding/json"
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

const DefaultProducerDir = "_snapshot-artifacts"
const DefaultDownloadDir = "_snapshot-downloads"
const ProducerManifest = "snapshot-artifact.json"

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

func (p Plan) Artifact(name string) (pipeline.PlannedArtifact, error) {
	if err := p.Validate(); err != nil {
		return pipeline.PlannedArtifact{}, err
	}
	for _, a := range p.Artifacts {
		if a.Name == name {
			return a, nil
		}
	}
	return pipeline.PlannedArtifact{}, fmt.Errorf("unplanned snapshot artifact %q", name)
}

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

func (p Plan) validateArtifacts() error {
	if len(p.Artifacts) > 0 {
		epoch, err := strconv.ParseInt(p.SourceDateEpoch, 10, 64)
		if err != nil || epoch < 0 {
			return fmt.Errorf("snapshot producers require frozen source_date_epoch")
		}
	}
	byName := map[string]pipeline.PlannedArtifact{}
	needed := map[string]bool{}
	for _, a := range p.Artifacts {
		configs := []struct {
			present bool
			kind    projecttype.Type
		}{{a.Go != nil, projecttype.Go}, {a.Cargo != nil, projecttype.Cargo}, {a.Maven != nil, projecttype.Maven}, {a.NPM != nil, projecttype.NPM}, {a.Gradle != nil, projecttype.Gradle}, {a.GradleAndroid != nil, projecttype.GradleAndroid}, {a.XcodeIOS != nil, projecttype.XcodeIOS}, {a.Python != nil, projecttype.Python}}
		for _, c := range configs {
			if c.present && c.kind != a.ProjectType {
				return fmt.Errorf("snapshot producer %s has foreign ecosystem configuration", a.Name)
			}
		}
		if !plainName(a.Name) || byName[a.Name].Name != "" || !pathsafe.Relative(a.WorkingDirectory) || filepath.ToSlash(filepath.Clean(a.WorkingDirectory)) != a.WorkingDirectory {
			return fmt.Errorf("invalid or duplicate snapshot producer %q", a.Name)
		}
		cfgArtifact := config.Artifact{Name: a.Name, ProjectType: a.ProjectType, WorkingDirectory: a.WorkingDirectory, BuildType: a.BuildType, PublishTo: a.PublishTo, SBOMs: a.SBOMs, RequireAuthorization: a.RequireAuthorization, Go: a.Go, Cargo: a.Cargo, Maven: a.Maven, NPM: a.NPM, Gradle: a.Gradle}
		cfg := &config.Config{Artifacts: []config.Artifact{cfgArtifact}}
		if err := config.Validate(cfg); err != nil {
			return err
		}
		if err := config.Derive(cfg); err != nil {
			return err
		}
		expected := pipeline.NewConfigPlan(cfg).Artifacts.All[0]
		actualJSON, _ := json.Marshal(a)
		expectedJSON, _ := json.Marshal(expected)
		if a.BuildArtifactName == "" || !bytes.Equal(actualJSON, expectedJSON) {
			return fmt.Errorf("snapshot producer %s disagrees with frozen typed configuration", a.Name)
		}
		switch a.ProjectType {
		case projecttype.Go, projecttype.Cargo, projecttype.Maven, projecttype.Gradle, projecttype.NPM:
		default:
			return fmt.Errorf("unsupported snapshot producer type %s", a.ProjectType)
		}
		byName[a.Name] = a
	}
	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			a, ok := byName[d.Name]
			if !ok || d.ArtifactName != a.BuildArtifactName || d.ProjectType != a.ProjectType || d.Destination != ArtifactDestination(a, b.Context) {
				return fmt.Errorf("snapshot dependency %s disagrees with frozen producer configuration", d.Name)
			}
			needed[d.Name] = true
			platforms := ""
			if a.Go != nil {
				platforms = a.Go.Platforms
			}
			if a.Cargo != nil {
				platforms = a.Cargo.Platforms
			}
			if platforms != "" && !slices.Contains(strings.FieldsFunc(platforms, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }), "linux/"+b.Arch) {
				return fmt.Errorf("snapshot producer %s does not build required linux/%s", a.Name, b.Arch)
			}
		}
	}
	if len(needed) != len(byName) {
		return fmt.Errorf("snapshot plan contains unneeded producers")
	}
	return nil
}

func (p Plan) ArtifactVersion() string {
	stem := strings.TrimSuffix(p.Tag, "-snapshot")
	index := strings.LastIndex(stem, "-")
	if index < 0 {
		return ""
	}
	return version.ComposeSnapshotVersion(p.BaseVersion, p.SourceBranch, stem[index+1:])
}

type ArtifactReceipt struct {
	Version      int                      `json:"version"`
	SourceSHA    string                   `json:"source_sha"`
	SourceBranch string                   `json:"source_branch"`
	PlanSHA256   string                   `json:"plan_sha256"`
	Artifact     pipeline.PlannedArtifact `json:"artifact"`
	Platforms    []string                 `json:"platforms"`
	Files        []DependencyFile         `json:"files"`
}
