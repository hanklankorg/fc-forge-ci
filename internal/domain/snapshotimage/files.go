// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

var (
	errNoFileProducer      = errors.New("has no snapshot build, so nothing is planned; a file snapshot builds go, cargo, maven, gradle and npm artifacts, set publish-files to false for this declaration")
	errUnplannedFile       = errors.New("unplanned snapshot file output")
	errInvalidFilesName    = errors.New("snapshot file output has no usable run artifact name")
	filesArtifactNameShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
)

// DefaultFilesDir is the directory a snapshot's file outputs are written to,
// one directory per artifact.
const DefaultFilesDir = "snapshot-files"

// FilesManifest is the file name of a file output's receipt, beside its files.
const FilesManifest = "snapshot-files.json"

// FilesRetentionDays is how long a forge keeps a snapshot's file outputs as
// run artifacts, where the instance's own limit is not shorter.
const FilesRetentionDays = 7

// FilesSigning is the signing of every file output: none. A snapshot holds no
// signing key, and its files carry no signature a release check would accept.
const FilesSigning = "NONE"

// FilesReceipt binds a file output to its source and lists its files. It is
// run-local integrity evidence written by the job that built the files, not a
// signature.
type FilesReceipt struct {
	Version         int              `json:"version"`
	SourceSHA       string           `json:"source_sha"`
	SourceBranch    string           `json:"source_branch"`
	SnapshotVersion string           `json:"snapshot_version"`
	Name            string           `json:"name"`
	ProjectType     projecttype.Type `json:"project_type"`
	Signing         string           `json:"signing"`
	Files           []DependencyFile `json:"files"`
}

// FileOutput is one planned file output as a workflow matrix reads it: the
// artifact, the toolchain that builds it and the run artifact that keeps it.
type FileOutput struct {
	Name         string           `json:"name"`
	ProjectType  projecttype.Type `json:"project_type"`
	ArtifactName string           `json:"artifact_name"`
}

// fileOutputs selects the declared artifacts a file snapshot keeps: every
// artifact its ecosystem builds outside an image. One compiled in a
// Containerfile, or one that builds nothing, has no file and is left out; one
// that builds a file no snapshot producer can build is refused.
func fileOutputs(cfg pipeline.ConfigPlan) ([]pipeline.PlannedArtifact, error) {
	out := []pipeline.PlannedArtifact{}

	for _, artifact := range cfg.Artifacts.All {
		if artifact.BuildArtifactName == "" {
			continue
		}

		if _, err := ToolchainFamily(artifact.ProjectType); err != nil {
			return nil, fmt.Errorf("artifact %s (%s) %w", artifact.Name, artifact.ProjectType, errNoFileProducer)
		}

		out = append(out, artifact)
	}

	return out, nil
}

// FileOutputs lists the planned file outputs with their run artifact names.
func (p Plan) FileOutputs() []FileOutput {
	out := make([]FileOutput, 0, len(p.Files))
	for _, file := range p.Files {
		out = append(out, FileOutput{Name: file.Name, ProjectType: file.ProjectType, ArtifactName: p.FilesArtifactName(file.Name)})
	}

	return out
}

// File validates the plan and returns the planned file output with the given name.
func (p Plan) File(name string) (pipeline.PlannedArtifact, error) {
	if err := p.Validate(); err != nil {
		return pipeline.PlannedArtifact{}, err
	}

	for _, file := range p.Files {
		if file.Name == name {
			return file, nil
		}
	}

	return pipeline.PlannedArtifact{}, fmt.Errorf("%w %q", errUnplannedFile, name)
}

// FilesArtifactName is the run artifact a file output is kept as: the
// artifact's name and the snapshot version, which always carries -snapshot-,
// the branch and the source commit, so the name cannot be a release's.
func (p Plan) FilesArtifactName(name string) string {
	return name + "-" + p.ArtifactVersion()
}

// validateFiles admits the planned file outputs: a frozen source timestamp, a
// tag that carries the source commit, and producers that agree with the typed
// configuration they were derived from.
func (p Plan) validateFiles() error {
	if len(p.Files) == 0 {
		return nil
	}

	if _, err := p.tagCommit(); err != nil {
		return err
	}

	if err := p.requireSourceDateEpoch(); err != nil {
		return err
	}

	byName := map[string]pipeline.PlannedArtifact{}

	for _, file := range p.Files {
		if err := validateProducer(file, byName); err != nil {
			return err
		}

		byName[file.Name] = file

		if !filesArtifactNameShape.MatchString(p.FilesArtifactName(file.Name)) {
			return fmt.Errorf("%w: %s", errInvalidFilesName, file.Name)
		}
	}

	return nil
}
