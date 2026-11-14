// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config_test

import (
	"bytes"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/output"
)

// The warning is for a container that is declared as built from an artifact
// forge-ci builds first. A container with no from, and one from a
// container-first Go or Cargo artifact, builds in the image by design.
func TestWarnContainerfileRebuilds_WarnsOnlyAboutPrebuiltArtifacts(t *testing.T) {
	t.Parallel()

	const declaration = `
artifacts:
  - {name: api, project-type: maven}
  - {name: tool, project-type: go}
  - {name: service, project-type: go, config: {build-mode: container-first}}
  - {name: worker, project-type: cargo, config: {build-mode: container-first}}
containers:
  - {name: api, from: [api], container-file: api/Containerfile}
  - {name: tool, from: [tool], container-file: tool/Containerfile}
  - {name: api-copy, from: [api], container-file: copy/Containerfile}
  - {name: api-web, from: [api], container-file: web/Containerfile}
  - {name: source, container-file: api/Containerfile}
  - {name: service, from: [service], container-file: tool/Containerfile}
  - {name: worker, from: [worker], container-file: worker/Containerfile}
`

	files := fstest.MapFS{
		".forge-ci/artifacts.yml": {Data: []byte(declaration)},
		"api/Containerfile":       {Data: []byte("FROM maven AS build\nRUN ./mvnw dependency:go-offline\nRUN ./mvnw clean package\n")},
		"tool/Containerfile":      {Data: []byte("FROM golang AS build\nRUN go build -o /tool .\n")},
		"copy/Containerfile":      {Data: []byte("FROM eclipse-temurin\n# RUN mvn package\nCOPY target/api.jar /app.jar\n")},
		"web/Containerfile":       {Data: []byte("FROM node\nRUN npm ci && npm run build\n")},
		"worker/Containerfile":    {Data: []byte("FROM rust\nRUN cargo build --release\n")},
	}

	var log, annotations bytes.Buffer

	summary := &fakeSummary{}

	appconfig.WarnContainerfileRebuilds(t.Context(), &log, output.NewAnnotator(&annotations, output.FormatGitHub), summary,
		appconfig.EmitConfigPlanInput{Path: ".forge-ci/artifacts.yml", FS: files})

	// The annotation stands on the build command, not on the dependency
	// fetch before it, and says what the image will hold.
	require.Equal(t, 2, bytes.Count(annotations.Bytes(), []byte("::warning ")), annotations.String())
	require.Contains(t, annotations.String(), "::warning file=api/Containerfile,line=3,")
	require.Contains(t, annotations.String(), "::warning file=tool/Containerfile,line=2,")
	require.Contains(t, annotations.String(), "container \"api\": api/Containerfile line 3 runs `./mvnw clean package`")
	require.Contains(t, annotations.String(), "not the artifact forge-ci built, tested, described in the SBOM and released")
	require.Contains(t, annotations.String(), "docs/ecosystems.md#use-the-built-artifact-in-the-image")

	for _, want := range []string{
		`The image of container "api" rebuilds what forge-ci built`,
		"api/Containerfile line 2: ./mvnw dependency:go-offline",
		"api/Containerfile line 3: ./mvnw clean package",
		`The image of container "tool" rebuilds what forge-ci built`,
		"fails when the package registry cannot be reached",
	} {
		require.Contains(t, log.String(), want)
	}

	for _, container := range []string{"api-copy", "api-web", "source", "service", "worker"} {
		require.NotContains(t, log.String()+annotations.String()+summary.buf.String(), `"`+container+`"`)
		require.NotContains(t, summary.buf.String(), "`"+container+"`")
	}

	require.Contains(t, summary.buf.String(), "Container `api`: `api/Containerfile` line 3 runs `./mvnw clean package`")
	require.Contains(t, summary.buf.String(), "Container `tool`")
}

// A repository with no declaration, or one the plan will refuse, is not this
// check's to fail.
func TestWarnContainerfileRebuilds_LeavesAnUnreadableDeclarationToThePlan(t *testing.T) {
	t.Parallel()

	for name, files := range map[string]fstest.MapFS{
		"absent":  {},
		"invalid": {".forge-ci/artifacts.yml": {Data: []byte("containers: {")}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var annotations bytes.Buffer

			appconfig.WarnContainerfileRebuilds(t.Context(), &bytes.Buffer{}, output.NewAnnotator(&annotations, output.FormatGitHub), nil,
				appconfig.EmitConfigPlanInput{Path: ".forge-ci/artifacts.yml", FS: files})

			require.Empty(t, annotations.String())
		})
	}
}
