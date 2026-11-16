// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// ArtifactRow is one artifact's matrix row: its name and the toolchain that
// builds it, which selects the job's image.
type ArtifactRow struct {
	Name        string           `json:"name"`
	ProjectType projecttype.Type `json:"project_type"`
}

// ArtifactMatrix is the planned artifact producers as matrix rows.
func (p Plan) ArtifactMatrix() pipeline.Matrix[ArtifactRow] {
	return artifactMatrix(p.Artifacts)
}

// FileMatrix is the planned file outputs as matrix rows. A job reads the run
// artifact its files are kept as with read-plan's files-artifact-name.
func (p Plan) FileMatrix() pipeline.Matrix[ArtifactRow] {
	return artifactMatrix(p.Files)
}

// BuildMatrix is the planned native builds as matrix rows.
func (p Plan) BuildMatrix() pipeline.Matrix[pipeline.BuildRow] {
	rows := make([]pipeline.BuildRow, 0, len(p.Builds))
	for _, build := range p.Builds {
		rows = append(rows, pipeline.BuildRow{Name: build.Name, Arch: build.Arch})
	}

	return pipeline.Matrix[pipeline.BuildRow]{Include: rows}
}

// ImageMatrix is the planned images as matrix rows.
func (p Plan) ImageMatrix() pipeline.Matrix[pipeline.ImageRow] {
	rows := make([]pipeline.ImageRow, 0, len(p.Images))
	for _, image := range p.Images {
		rows = append(rows, pipeline.ImageRow{Name: image.Name})
	}

	return pipeline.Matrix[pipeline.ImageRow]{Include: rows}
}

func artifactMatrix(artifacts []pipeline.PlannedArtifact) pipeline.Matrix[ArtifactRow] {
	rows := make([]ArtifactRow, 0, len(artifacts))
	for _, artifact := range artifacts {
		rows = append(rows, ArtifactRow{Name: artifact.Name, ProjectType: artifact.ProjectType})
	}

	return pipeline.Matrix[ArtifactRow]{Include: rows}
}
