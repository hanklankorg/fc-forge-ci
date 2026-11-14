// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

func TestContainerfileShortImageNames(t *testing.T) {
	t.Parallel()

	type names = []container.ShortImageName

	short := func(line int, image string) container.ShortImageName {
		return container.ShortImageName{Line: line, Image: image, Resolved: image}
	}

	tests := []struct {
		name  string
		given string
		want  names
	}{
		{"an official image by digest", "FROM eclipse-temurin@sha256:0123", names{short(1, "eclipse-temurin@sha256:0123")}},
		{"an official image by tag", "FROM --platform=$BUILDPLATFORM golang:1.27 AS build", names{short(1, "golang:1.27")}},
		{"an organisation's image", "FROM bitnami/nginx:1.29", names{short(1, "bitnami/nginx:1.29")}},
		{"a registry host", "FROM docker.io/library/eclipse-temurin:25\nFROM ghcr.io/example/app@sha256:0123", nil},
		{"a registry with a port, and localhost", "FROM registry:5000/app\nFROM localhost/app:1", nil},
		{"scratch is no image", "FROM scratch", nil},
		{
			"an earlier stage, by name and by index",
			"FROM docker.io/library/golang:1.27 AS Build\nFROM build AS test\nFROM 0\nCOPY --from=build /a /a\nCOPY --from=1 /b /b\n", nil,
		},
		{
			"a stage is not known before its FROM",
			"FROM later\nFROM docker.io/library/alpine:3 AS later", names{short(1, "later")},
		},
		{
			"an image in COPY --from and in a mount",
			"FROM scratch\nCOPY --from=busybox:1.37 /bin/sh /bin/sh\n" +
				"RUN --mount=type=bind,from=alpine:3,target=/mnt \\\n    --mount=type=cache,target=/c true\n" +
				"COPY --from=quay.io/example/tool:1 /tool /tool\n",
			names{short(2, "busybox:1.37"), short(3, "alpine:3")},
		},
		{
			"a build argument with a literal default",
			"ARG REGISTRY=docker.io/library\nARG BASE=${REGISTRY}/eclipse-temurin:25\nARG TAG=3\nFROM ${BASE}\nFROM alpine:$TAG\n",
			names{{Line: 5, Image: "alpine:$TAG", Resolved: "alpine:3"}},
		},
		{
			"a default written in the name",
			"FROM ${BASE:-node:24}\nFROM ${OTHER:-docker.io/library/node:24}",
			names{{Line: 1, Image: "${BASE:-node:24}", Resolved: "node:24"}},
		},
		{
			"a build argument with no default cannot be judged",
			"ARG BASE\nARG TAG=1\nFROM ${BASE}\nFROM $REGISTRY/app:${TAG}\n",
			names{
				{Line: 3, Image: "${BASE}", Resolved: "${BASE}", Unjudged: true},
				{Line: 4, Image: "$REGISTRY/app:${TAG}", Resolved: "$REGISTRY/app:${TAG}", Unjudged: true},
			},
		},
		{
			"a bare ARG in a stage keeps the default",
			"ARG BASE=docker.io/library/alpine:3\nFROM ${BASE}\nARG BASE\nCOPY --from=${BASE} /etc/os-release /\n", nil,
		},
		{
			"a comment and a file written by a here-document are no instructions",
			"# FROM alpine\nFROM docker.io/library/alpine:3\nCOPY <<EOF /Containerfile.example\nFROM alpine\nEOF\nFROM busybox\n",
			names{short(6, "busybox")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, container.ContainerfileShortImageNames(tt.given))
		})
	}
}

func TestShortImageNameDockerHubName(t *testing.T) {
	t.Parallel()

	for image, want := range map[string]string{
		"eclipse-temurin@sha256:0123": "docker.io/library/eclipse-temurin@sha256:0123",
		"golang:1.27":                 "docker.io/library/golang:1.27",
		"bitnami/nginx:1.29":          "docker.io/bitnami/nginx:1.29",
	} {
		require.Equal(t, want, container.ShortImageName{Resolved: image}.DockerHubName())
	}
}
