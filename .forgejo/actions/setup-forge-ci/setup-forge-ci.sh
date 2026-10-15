#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

# Makes the forge-ci binary of LAYER_ROOT (.forgejo/ of the checkout the
# action was fetched from) present in the job, downloading it by the recorded
# checksum when the job does not have it yet, verifies it against that
# checksum and an optional caller pin, then exposes it.
# Usage: setup-forge-ci.sh LAYER_ROOT

set -euo pipefail

layer_root=${1:-}
fail() {
	printf 'ERROR: setup-forge-ci: %s\n' "$1" >&2
	exit "${2:-1}"
}
[[ "${layer_root}" == /* && -d "${layer_root}" && ! -L "${layer_root}" ]] ||
	fail 'layer root must be an absolute, real directory'

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../../bin/lib-forge-ci.sh
source "${script_dir}/../../bin/lib-forge-ci.sh"
# The action verifies the checkout it was given, never an override.
# shellcheck disable=SC2034 # Read by verify_forge_ci.
HELPERS_ROOT=${layer_root}
unset FORGE_CI_BIN

case "${ADD_TO_PATH:-true}" in
true | false) ;;
*) fail 'add-to-path must be true or false' ;;
esac
if [[ -n "${EXPECTED_SHA256:-}" && ! "${EXPECTED_SHA256}" =~ ^[0-9a-f]{64}$ ]]; then
	fail 'expected-sha256 must be 64 lowercase hexadecimal characters'
fi
verify_forge_ci || exit 1
binary=${HELPERS_ENGINE_VERIFIED}
sha256=${HELPERS_ENGINE_SHA256}
if [[ -n "${EXPECTED_SHA256:-}" && "${EXPECTED_SHA256}" != "${sha256}" ]]; then
	fail 'forge-ci does not match expected-sha256'
fi

status=0
version=$("${binary}" --version) || status=$?
((status == 0)) || fail "forge-ci --version failed (exit ${status})" "${status}"
if [[ -z "${version}" || "${version}" == *[$'\n\r']* || "${#version}" -gt 256 ]]; then
	fail 'forge-ci returned unsafe or empty version output'
fi

if [[ "${ADD_TO_PATH:-true}" == true ]]; then
	path_file=${FORGEJO_PATH:-${GITHUB_PATH:-}}
	[[ -n "${path_file}" ]] || fail 'neither FORGEJO_PATH nor GITHUB_PATH is available'
	# The directory of the verified binary: a later step finds it there, and
	# the action's own directory does not outlive this step.
	printf '%s\n' "${binary%/*}" >>"${path_file}"
fi
output_file=${FORGEJO_OUTPUT:-${GITHUB_OUTPUT:-}}
if [[ -n "${output_file}" ]]; then
	printf 'binary-path=%s\nsha256=%s\nversion=%s\n' "${binary}" "${sha256}" "${version}" >>"${output_file}"
fi
# The version string is the same for every build of the layer; the digest
# says which one this is.
printf '%s sha256:%s\n' "${version}" "${sha256}"
