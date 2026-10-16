#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release build, called by the pinned release policy in a key-free job. It
# leaves the unsigned hand-off in dist/: the jar, its CycloneDX Build SBOM,
# release notes, checksums and the manifest. Forge CI's signer job signs and
# publishes it. The policy's version-bump: maven put the release version into
# the release commit; the build requires it there.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

maven_opts=(--batch-mode --no-transfer-progress --errors)
BUILD_TYPE=app MAVEN_CLI_OPTS="${maven_opts[*]}" CYCLONEDX_MAVEN_VERSION=2.9.1 JAVA_VERSION=25 \
	forge-ci build maven run --expect-version "${RELEASE_VERSION}"

jar="target/hello-maven-${RELEASE_VERSION}.jar"
test -f "${jar}"
mkdir -p dist
cp -- "${jar}" dist/
cp -- target/bom.json "dist/hello-maven-${RELEASE_VERSION}.cdx.json"

if ! forge-ci release notes --changelog-file CHANGELOG.md \
	--release-version "${RELEASE_TAG}" --target-file dist/release-notes.md; then
	forge-ci release notes --source-file /dev/null/absent --target-file dist/release-notes.md \
		--release-version "${RELEASE_TAG}" --release-commit "${RELEASE_SHA}"
fi
forge-ci release files declare \
	--asset 'dist/*.jar' \
	--sbom "dist/hello-maven-${RELEASE_VERSION}.cdx.json"

printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
