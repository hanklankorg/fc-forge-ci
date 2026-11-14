#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Sourced build glue only: Go owns validation/resolution and native argv.
# No eval, engine wrapper, enforcement receipt or caller-supplied flag string.
# First argument: the build caller's output array, or empty for exports only.
image_build_settings() {
  local output_array=$1
  shift
  local settings
  local -a fields
  settings=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." &&
    "${GO:-go}" run ./test/live/internal/livetest/preflight "$@") || return 2
  # Command substitution removes trailing empty fields on the legacy path.
  mapfile -t fields <<<"$settings"
  case "${fields[0]:-}" in
  default)
    export FORGE_CI_IMAGE_BUILD_BUDGET=default
    ;;
  required)
    [[ ${#fields[@]} == 16 ]] || return 2
    export FORGE_CI_IMAGE_BUILD_BUDGET=required
    export FORGE_CI_IMAGE_BUILD_CPUS="${fields[1]}"
    export FORGE_CI_IMAGE_BUILD_MEMORY_MIB="${fields[2]}"
    export FORGE_CI_IMAGE_BUILD_COMPILER_JOBS="${fields[3]}"
    export GOMAXPROCS="${fields[3]}" GOMEMLIMIT="${fields[2]}MiB"
    export GOFLAGS="${GOFLAGS:-} -p=${fields[3]}"
    printf 'image build: native limits requested; enforcement unmeasured; reserve host/non-RUN/disk separately\n' >&2
    ;;
  *) return 2 ;;
  esac
  if [[ -n "$output_array" ]]; then
    mapfile -s 4 -t "$output_array" <<<"$settings"
  fi
}
