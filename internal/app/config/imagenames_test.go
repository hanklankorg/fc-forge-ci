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

// Every declared container's Containerfile is read, with or without a from.
// A fully named image is silent, a shared Containerfile is reported once and
// a name that cannot be judged is said to be unchecked, not guessed at.
func TestWarnContainerfileShortImageNames(t *testing.T) {
	t.Parallel()

	const declaration = `
artifacts:
  - {name: api, project-type: maven}
containers:
  - {name: api, from: [api], container-file: api/Containerfile}
  - {name: api-debug, from: [api], container-file: api/Containerfile}
  - {name: web, container-file: web/Containerfile}
  - {name: full, container-file: full/Containerfile}
  - {name: argument, container-file: argument/Containerfile}
`

	files := fstest.MapFS{
		".forge-ci/artifacts.yml": {Data: []byte(declaration)},
		"api/Containerfile":       {Data: []byte("FROM eclipse-temurin@sha256:0123 AS run\nCOPY --from=bitnami/kubectl:1.31 /bin/kubectl /bin/\n")},
		"web/Containerfile":       {Data: []byte("FROM docker.io/library/node:22 AS build\nFROM scratch\nCOPY --from=build /app /app\nCOPY --from=nginx /etc/nginx /etc/nginx\n")},
		"full/Containerfile":      {Data: []byte("FROM cgr.dev/chainguard/static:latest\n")},
		"argument/Containerfile":  {Data: []byte("ARG BASE\nFROM ${BASE}\n")},
	}

	var log, annotations bytes.Buffer

	summary := &fakeSummary{}

	appconfig.WarnContainerfileShortImageNames(t.Context(), &log, output.NewAnnotator(&annotations, output.FormatGitHub), summary,
		appconfig.EmitConfigPlanInput{Path: ".forge-ci/artifacts.yml", FS: files})

	require.Equal(t, 2, bytes.Count(annotations.Bytes(), []byte("::warning ")), annotations.String())
	require.Contains(t, annotations.String(), "::warning file=api/Containerfile,line=1,")
	require.Contains(t, annotations.String(), "::warning file=web/Containerfile,line=4,")
	require.Contains(t, annotations.String(), "names the image `eclipse-temurin@sha256:0123` without a registry")
	require.Contains(t, annotations.String(), "Write the full name, `docker.io/library/eclipse-temurin@sha256:0123` for the Docker Hub image")
	require.Contains(t, annotations.String(), "docs/ecosystems.md#name-the-registry-of-an-image")

	for _, want := range []string{
		"api/Containerfile names an image without its registry",
		"  line 1: eclipse-temurin@sha256:0123\n    for the Docker Hub image write docker.io/library/eclipse-temurin@sha256:0123\n",
		"  line 2: bitnami/kubectl:1.31\n    for the Docker Hub image write docker.io/bitnami/kubectl:1.31\n",
		"  line 4: nginx\n",
		"argument/Containerfile line 2: `${BASE}` not checked for a registry",
	} {
		require.Contains(t, log.String(), want)
	}

	require.Equal(t, 1, bytes.Count(log.Bytes(), []byte("api/Containerfile names an image")), "a shared Containerfile is reported once")
	require.NotContains(t, log.String()+annotations.String()+summary.buf.String(), "full/Containerfile")
	require.NotContains(t, annotations.String()+summary.buf.String(), "argument/Containerfile")
	require.NotContains(t, log.String(), "docker.io/library/node")

	require.Contains(t, summary.buf.String(), "`api/Containerfile` line 1 names the image `eclipse-temurin@sha256:0123` without a registry")
	require.Contains(t, summary.buf.String(), "`web/Containerfile` line 4 names the image `nginx` without a registry")
}

// A repository with no declaration, or one the plan will refuse, is not this
// check's to fail.
func TestWarnContainerfileShortImageNames_LeavesAnUnreadableDeclarationToThePlan(t *testing.T) {
	t.Parallel()

	for name, files := range map[string]fstest.MapFS{
		"absent":  {},
		"invalid": {".forge-ci/artifacts.yml": {Data: []byte("containers: {")}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var annotations bytes.Buffer

			appconfig.WarnContainerfileShortImageNames(t.Context(), &bytes.Buffer{}, output.NewAnnotator(&annotations, output.FormatGitHub), nil,
				appconfig.EmitConfigPlanInput{Path: ".forge-ci/artifacts.yml", FS: files})

			require.Empty(t, annotations.String())
		})
	}
}
