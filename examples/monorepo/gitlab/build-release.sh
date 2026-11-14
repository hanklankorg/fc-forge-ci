#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# Release build, the release policy's `build` input, run in key-free jobs. It
# leaves the unsigned hand-off in dist/: the backend's jar and the frontend's
# packed package, each with its CycloneDX Build SBOM, release notes, checksums
# and the manifest. The runtime images are the release policy's: its image
# jobs build the containers of .forge-ci/artifacts.yml over the release
# checkout with image-context/ laid on top, the jar where the backend's
# Containerfile copies it from and the frontend's package unpacked at the
# root. The release plan's version bump put the release version into both
# projects in the release commit; each build requires it there.
# The policy runs it once per toolchain family of the plan, in that family's
# runtime image: with FORGE_CI_BUILD_FAMILY=java it builds the backend, with
# node the frontend, each into dist/ and image-context/. It runs it once more
# in the release files job, with the variable empty, over both builds'
# dist/, where it writes the release notes and the manifest.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

case "${FORGE_CI_BUILD_FAMILY:-}" in
java)
  maven_opts=(--batch-mode --no-transfer-progress --errors)
  BUILD_TYPE=app MAVEN_CLI_OPTS="${maven_opts[*]}" CYCLONEDX_MAVEN_VERSION=2.9.1 JAVA_VERSION=25 \
    forge-ci build maven run --working-dir services/backend --expect-version "${RELEASE_VERSION}"

  jar="services/backend/target/backend-api-${RELEASE_VERSION}.jar"
  test -f "${jar}"
  mkdir -p dist image-context/services/backend/target
  cp -- "${jar}" dist/
  cp -- "${jar}" image-context/services/backend/target/
  cp -- services/backend/target/bom.json "dist/backend-api-${RELEASE_VERSION}.cdx.json"
  ;;
node)
  # The frontend's tests gate here, where Node is: the policy's checks run in
  # its one job image, the Java one, and the build below only warns when a
  # test fails. A failing test stops the release before anything is signed.
  npm --prefix packages/frontend ci
  npm --prefix packages/frontend test

  NODE_VERSION=24 CYCLONEDX_VERSION=6.0.1 \
    forge-ci build npm run --working-dir packages/frontend --expect-version "${RELEASE_VERSION}"

  # npm pack names a scoped package <scope>-<name>-<version>.tgz.
  set -- packages/frontend/*-"${RELEASE_VERSION}".tgz
  if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
    printf 'build-release.sh: want one packed package of %s in packages/frontend, got: %s\n' "${RELEASE_VERSION}" "$*" >&2
    exit 1
  fi
  # The image is built from the package unpacked, as GitHub's container job
  # and Forgejo's release-images build it.
  mkdir -p dist image-context
  tar -xzf "$1" --strip-components=1 -C image-context
  cp -- "$1" dist/
  cp -- packages/frontend/bom.json "dist/frontend-ui-${RELEASE_VERSION}.cdx.json"
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
    --asset 'dist/*.tgz' \
    --sbom 'dist/*.cdx.json'
  ;;
*)
  printf 'build-release.sh: no %s build here\n' "${FORGE_CI_BUILD_FAMILY}" >&2
  exit 1
  ;;
esac

printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
