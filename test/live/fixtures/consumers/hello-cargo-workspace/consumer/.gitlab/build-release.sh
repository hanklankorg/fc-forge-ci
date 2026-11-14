#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release build, called by the pinned release policy in a key-free job; the
# tests ran in the checks. The Containerfile is the build: its builder stage
# compiles both workspace binaries, and the policy's export jobs
# (export-release.sh) have laid them, per selected platform, into dist/. It
# leaves the unsigned hand-off in dist/: those binaries, release notes,
# checksums and the manifest. The two runtime images are the release
# policy's: its image jobs build .forge-ci/release-images.yml from the
# release checkout alone.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

mkdir -p dist
if ! forge-ci release notes --changelog-file CHANGELOG.md \
  --release-version "${RELEASE_TAG}" --target-file dist/release-notes.md; then
  forge-ci release notes --source-file /dev/null/absent --target-file dist/release-notes.md \
    --release-version "${RELEASE_TAG}" --release-commit "${RELEASE_SHA}"
fi

# extract.binary: the export jobs' binaries, dist/linux-<arch>/<name>-linux-<arch>.
forge-ci release files declare \
  --asset "dist/linux-*/hello-worker-linux-*" \
  --asset "dist/linux-*/hello-keytool-linux-*"

printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
