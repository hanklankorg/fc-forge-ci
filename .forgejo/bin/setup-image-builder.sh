#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

# Shared exec glue for the composite action and callable workflows that fetch
# their own pinned layer. Policy and engine effects stay behind the CLI.
set -euo pipefail
layer=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=../../.engine/bin/lib-forge-ci.sh
source "$layer/.engine/bin/lib-forge-ci.sh"
engine_repository=${FORGE_CI_REPOSITORY_URL:-}
if [ -n "${GITHUB_ACTION_REPOSITORY:-}" ]; then
  engine_repository="${GITHUB_SERVER_URL%/}/${GITHUB_ACTION_REPOSITORY}"
fi
run_forge_ci container setup-image-builder \
  --image-builder "${CI_IMAGE_BUILDER:-buildah}" \
  --job-image "${CI_IMAGE_BUILDER_JOB_IMAGE:-}" \
  --engine-repository "$engine_repository" \
  --install-packages=true --probe-build=true --print-store=false --summary=false \
  --extra-packages "${SETUP_BUILDAH_EXTRA_PACKAGES:-}"
