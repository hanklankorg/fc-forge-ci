#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# extract.binary, run by the pinned release policy's export jobs: once per
# selected platform, natively, with the image builder the policy selects,
# Buildah or kaniko, in its job image. The Containerfile's export-binary
# stage is exported as files, its binary renamed <name>-linux-<arch>, into
# dist/, which the build job declares.
set -euo pipefail

: "${EXPORT_PLATFORM:?}" "${EXPORT_ARCH:?}" "${RELEASE_SHA:?}"
# Before the build: kaniko replaces this container's root filesystem, git
# with it.
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

name=hello-cargo-container

forge-ci container build --mode local --target export-binary \
  --file Containerfile --context . --platform "${EXPORT_PLATFORM}" \
  --output-dir "dist/linux-${EXPORT_ARCH}"
forge-ci container suffix-extracted-binaries \
  --binaries-dir "dist/linux-${EXPORT_ARCH}" --arch "${EXPORT_ARCH}" --expected-names "${name}"
