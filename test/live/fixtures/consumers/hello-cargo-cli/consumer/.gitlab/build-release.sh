#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release build, called by the pinned release policy in key-free jobs. It
# leaves the unsigned hand-off in dist/: the binary, the CycloneDX Build
# SBOM, release notes, checksums and the manifest. Forge CI's signer job
# signs and publishes it. There is no container: cargo compiles the command.
# The policy runs it twice: in its rust build job, with
# FORGE_CI_BUILD_FAMILY=rust, it builds into dist/; in the release files job,
# with the variable empty, over that job's dist/, it writes the release notes
# and the manifest.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

name=hello-cargo-cli

case "${FORGE_CI_BUILD_FAMILY:-}" in
rust)
  # The platform of the job alone: the toolchain pin lists no other target.
  forge-ci build cargo run --version "${RELEASE_TAG}" --commit "${RELEASE_SHA}" \
    --artifact-name "${name}" --platforms linux/amd64
  cp -- bom.json "dist/${name}.cdx.json"
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
