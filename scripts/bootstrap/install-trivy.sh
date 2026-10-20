#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install Trivy vulnerability scanner if not already available.
#
# Usage: source this file, then call install_trivy
#   source "$(dirname "$0")/../bootstrap/install-trivy.sh"
#   install_trivy

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=github-releases depName=aquasecurity/trivy
readonly TRIVY_VERSION="v0.76.0"
# Archive hashes are independently pinned here rather than downloaded beside
# the archive. Source: the upstream v0.76.0 release checksums asset.
readonly TRIVY_SHA256_LINUX_AMD64="e24b1c51293c1b15e4ba8d45bbc7891ba053f90278ec2698f3562c993e145fe7"
readonly TRIVY_SHA256_LINUX_ARM64="aab21e677a7b7071fc79f1711480840754c18a1b9d9cefa7ccd06af63a0e8f4c"
readonly TRIVY_SHA256_DARWIN_AMD64="89563d4d495cf4ef06a0d1950f8710b124e9f7bd2615ec6c47b03673f4ad45a2"
readonly TRIVY_SHA256_DARWIN_ARM64="e7ec0d21fe842553f59af99aba2efbcc822369fbaed27019c575408b6d135fce"

install_trivy() {
	if command -v trivy &>/dev/null; then
		ci_print_already_installed "Trivy" "$(trivy --version 2>/dev/null | head -1)"
		return 0
	fi

	local install_dir os arch asset sha256 archive version
	install_dir="$(ci_install_dir trivy)"
	mkdir -p "$install_dir"
	version="${TRIVY_VERSION#v}"

	case "$(uname -s):$(uname -m)" in
	Linux:x86_64)
		os=Linux
		arch=64bit
		sha256="$TRIVY_SHA256_LINUX_AMD64"
		;;
	Linux:aarch64 | Linux:arm64)
		os=Linux
		arch=ARM64
		sha256="$TRIVY_SHA256_LINUX_ARM64"
		;;
	Darwin:x86_64)
		os=macOS
		arch=64bit
		sha256="$TRIVY_SHA256_DARWIN_AMD64"
		;;
	Darwin:arm64)
		os=macOS
		arch=ARM64
		sha256="$TRIVY_SHA256_DARWIN_ARM64"
		;;
	*)
		printf 'ERROR: unsupported platform for Trivy: %s/%s\n' "$(uname -s)" "$(uname -m)" >&2
		return 1
		;;
	esac
	asset="trivy_${version}_${os}-${arch}.tar.gz"
	archive="$install_dir/$asset"

	printf "Installing Trivy vulnerability scanner (version: %s)...\n" "${TRIVY_VERSION}"
	if ! ci_download_verified "https://github.com/aquasecurity/trivy/releases/download/${TRIVY_VERSION}/${asset}" "$archive" "$sha256" ||
		! tar -xzf "$archive" -C "$install_dir" trivy; then
		rm -f "$archive" "$install_dir/trivy"
		printf "ERROR: Failed to install Trivy %s\n" "${TRIVY_VERSION}" >&2
		return 1
	fi
	rm -f "$archive"

	ci_prepend_path "$install_dir"
	ci_require_command trivy Trivy || return 1
	ci_print_installed "Trivy" "$(trivy --version 2>/dev/null | head -1)"
}
