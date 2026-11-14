#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release build, called by the pinned release policy in key-free jobs. It
# leaves the unsigned hand-off in dist/: the jar, its CycloneDX Build SBOM,
# release notes, checksums and the manifest. The runtime image is the release
# policy's: its image jobs build the containers of .forge-ci/artifacts.yml
# over the release checkout with image-context/, the jar, laid on top. The
# release plan's version bump put the release version into the release
# commit; the build requires it there.
# The policy runs it twice: in its java build job, with
# FORGE_CI_BUILD_FAMILY=java, it builds into dist/ and image-context/; in the
# release files job, with the variable empty, over every build job's dist/, it
# writes the release notes and the manifest.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

case "${FORGE_CI_BUILD_FAMILY:-}" in
java)
  maven_opts=(--batch-mode --no-transfer-progress --errors)
  BUILD_TYPE=app MAVEN_CLI_OPTS="${maven_opts[*]}" CYCLONEDX_MAVEN_VERSION=2.9.1 JAVA_VERSION=25 \
    forge-ci build maven run --expect-version "${RELEASE_VERSION}"

  jar="target/hello-maven-service-${RELEASE_VERSION}.jar"
  test -f "${jar}"
  mkdir -p dist image-context/target
  cp -- "${jar}" dist/
  cp -- "${jar}" image-context/target/
  cp -- target/bom.json "dist/hello-maven-service-${RELEASE_VERSION}.cdx.json"
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
    --sbom "dist/hello-maven-service-${RELEASE_VERSION}.cdx.json"
  ;;
*)
  printf 'build-release.sh: no %s build here\n' "${FORGE_CI_BUILD_FAMILY}" >&2
  exit 1
  ;;
esac

printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
