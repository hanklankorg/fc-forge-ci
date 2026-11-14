#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release build, called by the pinned release policy in key-free jobs. It
# leaves the unsigned hand-off in dist/: one binary per platform, the
# CycloneDX Build SBOM, release notes, checksums and the manifest. Forge CI's
# signer job signs and publishes it. A Go module has no version file: the
# release version and commit are stamped into the binary.
# The policy runs it twice: in its go build job, with
# FORGE_CI_BUILD_FAMILY=go, it builds into dist/; in the release files job,
# with the variable empty, over every build job's dist/, it writes the release
# notes and the manifest.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

name=my-cli

case "${FORGE_CI_BUILD_FAMILY:-}" in
go)
  forge-ci build go run --version "${RELEASE_TAG}" --commit "${RELEASE_SHA}" \
    --artifact-name "${name}" --main-package "./cmd/${name}" \
    --platforms linux/amd64,linux/arm64
  cp -- ".forge-ci/go-build-sbom/${name}/bom.json" "dist/${name}.cdx.json"
  ;;
'')
  mkdir -p dist
  if ! forge-ci release notes --changelog-file CHANGELOG.md \
    --release-version "${RELEASE_TAG}" --target-file dist/release-notes.md; then
    forge-ci release notes --source-file /dev/null/absent --target-file dist/release-notes.md \
      --release-version "${RELEASE_TAG}" --release-commit "${RELEASE_SHA}"
  fi
  forge-ci release files declare \
    --asset 'dist/*/*' \
    --sbom "dist/${name}.cdx.json"
  ;;
*)
  printf 'build-release.sh: no %s build here\n' "${FORGE_CI_BUILD_FAMILY}" >&2
  exit 1
  ;;
esac

printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
