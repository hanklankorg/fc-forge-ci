#!/bin/bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Trusted leaf exec glue, not a downloader or an identity-selection policy.
# Invoke from an administrator-pinned copy, before consumer checkout/secrets.
# See docs/engine-delivery.md for tool placement and runner prerequisites.
set -euo pipefail
export PATH=/usr/bin:/bin
unset FORGE_CI_BIN FORGE_CI_PATH FORGE_CI_BINARY_REF FORGE_CI_PLAN

fail() {
  printf 'trusted-bootstrap: %s\n' "$1" >&2
  exit 78
}
[[ $# -ge 7 && "$6" == -- ]] || fail 'expected CHECKOUT HTTPS_ORIGIN FULL_COMMIT ABSOLUTE_BINARY TOOL_PROFILE -- CLI_ARGUMENT...'
checkout=$1 origin=$2 commit=$3 binary=$4 profile=$5
shift 6
[[ "$profile" =~ ^[a-z][a-z-]*$ ]] || fail 'tool profile must be one literal profile name'
[[ "$commit" =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]] || fail 'engine commit must be full and lowercase'
[[ "$origin" =~ ^https://[^/?#@[:space:]]+/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail 'engine origin must be an explicit HTTPS repository URL'
[[ "$checkout" == /* && "$binary" == /* ]] || fail 'checkout and binary paths must be absolute'
case "$(uname -s)/$(uname -m)" in
Linux/x86_64 | Linux/aarch64) ;;
*) fail 'only native linux/amd64 and linux/arm64 are supported' ;;
esac
[[ -d "$checkout" && ! -L "$checkout" && "$(realpath -e -- "$checkout")" == "$checkout" ]] || fail 'engine checkout must be a canonical non-symlink directory'
[[ -f "$binary" && ! -L "$binary" && -x "$binary" && "$(realpath -e -- "$binary")" == "$binary" ]] || fail 'delivery binary must be an available executable regular non-symlink file'

# Read only metadata/objects from the caller's trusted local checkout. These
# plumbing commands do not inspect index/worktree cleanliness, run filters or
# hooks, or traverse submodules. Disable paging and lazy object fetching too.
# This is not authentication of an arbitrary attacker-supplied Git database.
engine_git() {
  env -i PATH=/usr/bin:/bin HOME=/nonexistent GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_NO_LAZY_FETCH=1 \
    git --no-pager --no-replace-objects -C "$checkout" "$@"
}
[[ "$(engine_git rev-parse --show-toplevel)" == "$checkout" ]] || fail 'not the engine checkout root'
[[ "$(engine_git rev-parse --verify HEAD)" == "$commit" ]] || fail 'engine checkout commit differs from approval'
actual_origin=$(engine_git config --local --no-includes --get remote.origin.url) || fail 'engine origin is missing'
[[ "${actual_origin%.git}" == "${origin%.git}" ]] || fail 'engine checkout origin differs from approval'
# Compare only the exact files consumed below with approved Git objects,
# hashing raw bytes without filters, independently of index flags/attributes.
# Whole-checkout cleanliness is the fresh trusted checkout caller's obligation;
# do not use status/diff here (fsmonitor, filters and submodules can run code).
# No selected-checkout shell is sourced until identity AND contents pass.
for relative in .engine/bin/lib-forge-ci.sh .engine/bin/forge-ci-linux-amd64.sha256 .engine/bin/forge-ci-linux-arm64.sha256 scripts/ci/trusted-runtime-tools.sh; do
  file=$checkout/$relative
  [[ -f "$file" && ! -L "$file" && "$(realpath -e -- "$file")" == "$file" ]] || fail "unsafe engine file: $relative"
  mode=$(engine_git ls-tree "$commit" -- "$relative")
  [[ "$mode" == '100644 blob '* || "$mode" == '100755 blob '* ]] || fail "engine file is not a committed regular file: $relative"
  [[ "$(engine_git hash-object --no-filters -- "$file")" == "$(engine_git rev-parse "$commit:$relative")" ]] || fail "engine file differs from approved commit: $relative"
done

# Existing hash owner, strict override, no fetch fallback. Source-time globals
# reset the verifier cache; no caller-supplied digest or cached verification.
# shellcheck source=../../.engine/bin/lib-forge-ci.sh
source "$checkout/.engine/bin/lib-forge-ci.sh"
# shellcheck disable=SC2034 # Read by verify_forge_ci in lib-forge-ci.sh.
SIGNER_STRICT=1
# shellcheck disable=SC2034 # Read by verify_forge_ci in lib-forge-ci.sh.
FORGE_CI_BIN=$binary
verify_forge_ci || exit $?
unset FORGE_CI_BIN
# Admit tool placement only AFTER the original absolute CLI's hash verification.
# The admitted helper owns the finite profile set and refuses an unknown name.
# Never discover forge-ci on the image's PATH (which may contain an older SDK).
exec /bin/bash -p "$checkout/scripts/ci/trusted-runtime-tools.sh" "$profile" "$HELPERS_ENGINE_VERIFIED" "$@"
