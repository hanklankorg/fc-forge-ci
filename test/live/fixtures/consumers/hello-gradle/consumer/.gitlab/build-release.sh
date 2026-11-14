#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release build, called by the pinned release policy in key-free jobs. It
# leaves the unsigned hand-off in dist/: the library's jars, its CycloneDX
# Build SBOM, release notes, checksums and the manifest. Forge CI's signer job
# signs and publishes it. The release plan's version bump put the release
# version into gradle.properties in the release commit; the jar names carry
# it, so the build requires it there.
# The policy runs it twice: in its java build job, with
# FORGE_CI_BUILD_FAMILY=java, it builds into dist/; in the release files job,
# with the variable empty, over every build job's dist/, it writes the release
# notes and the manifest.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

case "${FORGE_CI_BUILD_FAMILY:-}" in
java)
  GRADLE_TASKS=build JAVA_VERSION=25 CYCLONEDX_GRADLE_VERSION=3.5.1 forge-ci build gradle run

  jar="build/libs/my-gradle-lib-${RELEASE_VERSION}.jar"
  test -f "${jar}"
  mkdir -p dist
  cp -- "${jar}" "build/libs/my-gradle-lib-${RELEASE_VERSION}-sources.jar" \
    "build/libs/my-gradle-lib-${RELEASE_VERSION}-javadoc.jar" dist/
  cp -- build/reports/cyclonedx/bom.json "dist/my-gradle-lib-${RELEASE_VERSION}.cdx.json"
  ;;
'')
  mkdir -p dist
  if ! forge-ci release notes --changelog-file CHANGELOG.md \
    --release-version "${RELEASE_TAG}" --target-file dist/release-notes.md; then
    forge-ci release notes --source-file /dev/null/absent --target-file dist/release-notes.md \
      --release-version "${RELEASE_TAG}" --release-commit "${RELEASE_SHA}"
  fi
  forge-ci release files declare \
    --asset 'dist/*.jar' \
    --sbom "dist/my-gradle-lib-${RELEASE_VERSION}.cdx.json"
  ;;
*)
  printf 'build-release.sh: no %s build here\n' "${FORGE_CI_BUILD_FAMILY}" >&2
  exit 1
  ;;
esac

printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
