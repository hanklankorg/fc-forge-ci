#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release files, called by the pinned release policy in a key-free job. The
# project is declared project-type: meta, so the plan has no build job of a
# toolchain family and the policy runs this once, in the release files job.
# Nothing is built. It leaves the unsigned hand-off in dist/: the source
# archive of the release commit, release notes, checksums and the manifest.
# Forge CI's signer job signs and publishes it; the signer signs files, so a
# release hands at least one over.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

if [ -n "${FORGE_CI_BUILD_FAMILY:-}" ]; then
  printf 'build-release.sh: no %s build here\n' "${FORGE_CI_BUILD_FAMILY}" >&2
  exit 1
fi

mkdir -p dist
git archive --format=tar.gz --prefix="source-${RELEASE_TAG}/" \
  --output="dist/source-${RELEASE_TAG}.tar.gz" HEAD
if ! forge-ci release notes --changelog-file CHANGELOG.md \
  --release-version "${RELEASE_TAG}" --target-file dist/release-notes.md; then
  forge-ci release notes --source-file /dev/null/absent --target-file dist/release-notes.md \
    --release-version "${RELEASE_TAG}" --release-commit "${RELEASE_SHA}"
fi
forge-ci release files declare --asset "dist/source-${RELEASE_TAG}.tar.gz"

printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
