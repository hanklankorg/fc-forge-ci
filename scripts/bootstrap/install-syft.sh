#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the repository-pinned Syft SBOM generator, independent of PATH.
#
# Usage: source this file, then call install_syft
#   source "$(dirname "$0")/../bootstrap/install-syft.sh"
#   install_syft

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=github-releases depName=anchore/syft
readonly SYFT_VERSION="v1.51.1"
# Archive hashes are independently pinned here rather than downloaded beside
# the archive. Source: the upstream v1.51.1 release checksums asset.
readonly SYFT_SHA256_LINUX_AMD64="8fcb33017a0dc1058298c923c436d19dfa68ae93968e0b423248542e3afb9fc3"
readonly SYFT_SHA256_LINUX_ARM64="a7fd2b784e6664acd44719270574f6cd8c6864fc2b1700bf9099bd1cccda7d7f"
readonly SYFT_SHA256_DARWIN_AMD64="0e186ce1d4351ec276126851ca3ff258ed070e93e73574ed64858d4fc2339867"
readonly SYFT_SHA256_DARWIN_ARM64="ac063af3b9874769deb7ea1e6d76841e68f9e3bb50cd654226fc977de65532c1"

install_syft() {
	local install_dir os arch asset sha256 archive version
	install_dir="$(ci_install_dir syft)"
	if [[ -L "$install_dir" || (-e "$install_dir" && ! -d "$install_dir") ]]; then
		printf 'ERROR: Syft install directory is not a real directory: %s\n' "$install_dir" >&2
		return 1
	fi
	mkdir -p "$install_dir"
	chmod 0700 "$install_dir"
	version="${SYFT_VERSION#v}"

	case "$(uname -s):$(uname -m)" in
	Linux:x86_64)
		os=linux
		arch=amd64
		sha256="$SYFT_SHA256_LINUX_AMD64"
		;;
	Linux:aarch64 | Linux:arm64)
		os=linux
		arch=arm64
		sha256="$SYFT_SHA256_LINUX_ARM64"
		;;
	Darwin:x86_64)
		os=darwin
		arch=amd64
		sha256="$SYFT_SHA256_DARWIN_AMD64"
		;;
	Darwin:arm64)
		os=darwin
		arch=arm64
		sha256="$SYFT_SHA256_DARWIN_ARM64"
		;;
	*)
		printf 'ERROR: unsupported platform for Syft: %s/%s\n' "$(uname -s)" "$(uname -m)" >&2
		return 1
		;;
	esac
	asset="syft_${version}_${os}_${arch}.tar.gz"
	archive="$install_dir/$asset"

	printf "Installing Syft SBOM generator (version: %s)...\n" "${SYFT_VERSION}"
	if ! ci_download_verified "https://github.com/anchore/syft/releases/download/${SYFT_VERSION}/${asset}" "$archive" "$sha256" ||
		! tar -xzf "$archive" -C "$install_dir" syft; then
		rm -f "$archive" "$install_dir/syft"
		printf "ERROR: Failed to install Syft %s\n" "${SYFT_VERSION}" >&2
		return 1
	fi
	rm -f "$archive"

	ci_prepend_path "$install_dir"
	ci_require_command syft Syft || return 1
	ci_print_installed "Syft" "$(syft version --output json 2>/dev/null | grep -o '"version":[[:space:]]*"[^"]*"' | cut -d'"' -f4)"
}
