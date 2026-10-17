#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

set -euo pipefail

missing_tools=()
required_tools=(
	bash
	git
	curl
	jq
	gpg
	cosign
	skopeo
	syft
	tar
	sha256sum
	mktemp
	dirname
	find
	awk
	grep
	sed
	tr
	cut
	sort
	wc
)

for tool in "${required_tools[@]}"; do
	if ! command -v "${tool}" >/dev/null 2>&1; then
		missing_tools+=("${tool}")
	fi
done

if [ "${#missing_tools[@]}" -gt 0 ]; then
	echo "verify-signer-toolchain: signer toolchain is missing required tools: ${missing_tools[*]}" >&2
	echo "Use the pinned signer image instead of installing tools at runtime." >&2
	exit 1
fi

if [ "${SIGNER_STRICT:-0}" = "1" ]; then
	forbidden_tools=(sudo gh bun python python3 ruby node npm mise)
	found_forbidden=()
	for tool in "${forbidden_tools[@]}"; do
		if command -v "${tool}" >/dev/null 2>&1; then
			found_forbidden+=("${tool}")
		fi
	done
	if [ "${#found_forbidden[@]}" -gt 0 ]; then
		echo "verify-signer-toolchain: signer image contains forbidden tools: ${found_forbidden[*]}" >&2
		exit 1
	fi

	# DELIBERATELY self-contained (no lib-forge-ci.sh sourcing): this is a
	# fail-closed boundary check, and a boundary check that depends on the very
	# helper library it is auditing could be blinded by a compromised lib.
	#
	# Signing-boundary policy: the forge-ci Go CLI is the ONE non-shell executable
	# that may run in the signing boundary — and only when its sha256 matches the
	# recorded pin of the host's architecture (bin/forge-ci-linux-<arch>.sha256)
	# in the SHA-verified helpers checkout.
	# The checkout's commit is pinned by EXPECTED_HELPERS_SHA, so the pin file is
	# exactly as reviewed as an inline workflow copy — without a second value
	# that can drift. Check the explicit resolver override, PATH, and the helper
	# checkout's own binary; any of them can be executed by wrappers.
	#
	# The checkout tracks the pin and not the binary: a job downloads it in a
	# step of its own, before this check. So where the pin is, in a helper
	# checkout, the binary must be there too. Without it the first engine call
	# would have to download it in a step that holds a key, after this check
	# has passed. The signer image build runs this script with no checkout
	# beside it, and there is neither.
	forge_ci_bins=()
	[ -z "${FORGE_CI_BIN:-}" ] || forge_ci_bins+=("${FORGE_CI_BIN}")
	path_forge_ci_bin="$(command -v forge-ci 2>/dev/null || true)"
	[ -z "${path_forge_ci_bin}" ] || forge_ci_bins+=("${path_forge_ci_bin}")
	script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
	layer_forge_ci_bin="${script_dir}/../bin/forge-ci"
	case "$(uname -m)" in
	x86_64 | amd64) forge_ci_arch=amd64 ;;
	aarch64 | arm64) forge_ci_arch=arm64 ;;
	*)
		echo "verify-signer-toolchain: no forge-ci binary is recorded for this host's architecture: $(uname -m)" >&2
		exit 1
		;;
	esac
	forge_ci_pin_file="${script_dir}/../bin/forge-ci-linux-${forge_ci_arch}.sha256"
	if [ -e "${layer_forge_ci_bin}" ] || [ -L "${layer_forge_ci_bin}" ]; then
		forge_ci_bins+=("${layer_forge_ci_bin}")
	elif [ -e "${forge_ci_pin_file}" ] || [ -L "${forge_ci_pin_file}" ]; then
		echo "verify-signer-toolchain: the layer's forge-ci binary is missing: ${layer_forge_ci_bin}" >&2
		echo "  A signer job downloads it in its 'Fetch engine binary' step, before this check." >&2
		exit 1
	fi

	for forge_ci_bin in "${forge_ci_bins[@]}"; do
		if [ ! -f "${forge_ci_pin_file}" ] || [ -L "${forge_ci_pin_file}" ]; then
			echo "verify-signer-toolchain: a 'forge-ci' binary is present in the signer but the recorded pin is missing." >&2
			echo "  Expected ${forge_ci_pin_file}, or remove the binary." >&2
			echo "  An unpinned executable in the signing boundary fails closed." >&2
			exit 1
		fi
		recorded_forge_ci_sha="$(awk '{print $1}' "${forge_ci_pin_file}")"
		actual_forge_ci_sha="$(sha256sum -- "${forge_ci_bin}" | awk '{print $1}')"
		if [ "${actual_forge_ci_sha}" != "${recorded_forge_ci_sha}" ]; then
			echo "verify-signer-toolchain: forge-ci binary does not match the recorded pin." >&2
			echo "  recorded: ${recorded_forge_ci_sha}" >&2
			echo "  actual:   ${actual_forge_ci_sha}" >&2
			exit 1
		fi
		echo "forge-ci binary matches the recorded pin (${actual_forge_ci_sha}): ${forge_ci_bin}"
	done
fi

jq --version
gpg --version | sed -n '1p'
cosign version
skopeo --version
syft version
