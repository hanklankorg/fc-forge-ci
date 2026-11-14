#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# Release files, the release policy's `build` input, run in a key-free job
# once every base stage is promoted. The project is declared project-type:
# meta, so the plan has no build job of a toolchain family and this runs
# once, in the release files job.
#
# The base stage hands each promoted image on as
# ${FORGE_CI_BASE_IMAGES_DIR}/<flavor>.json, {"ref":...,"input_id":...} with
# the ref pinned by digest. The release's one file, image-inventory.json,
# names every image by flavor.
set -euo pipefail
umask 077
export LC_ALL=C

: "${RELEASE_TAG:?}" "${RELEASE_SHA:?}" "${FORGE_CI_BASE_IMAGES_DIR:?}"

if [ -n "${FORGE_CI_BUILD_FAMILY:-}" ]; then
  printf 'build-release.sh: no %s build here\n' "${FORGE_CI_BUILD_FAMILY}" >&2
  exit 1
fi

mkdir -p dist
inventory=''
for flavor in base tools debug; do
  inventory+=$(printf '%s"%s":%s' "${inventory:+,}" "${flavor}" "$(cat -- "${FORGE_CI_BASE_IMAGES_DIR}/${flavor}.json")")
done
printf '{%s}\n' "${inventory}" >dist/image-inventory.json

if ! forge-ci release notes --changelog-file CHANGELOG.md \
  --release-version "${RELEASE_TAG}" --target-file dist/release-notes.md; then
  forge-ci release notes --source-file /dev/null/absent --target-file dist/release-notes.md \
    --release-version "${RELEASE_TAG}" --release-commit "${RELEASE_SHA}"
fi
forge-ci release files declare --asset dist/image-inventory.json
