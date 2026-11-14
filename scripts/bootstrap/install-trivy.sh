#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the pinned Trivy vulnerability scanner, unless that version is already on PATH.
#
# Usage: source this file, then call install_trivy
#   source "$(dirname "$0")/../bootstrap/install-trivy.sh"
#   install_trivy

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=github-releases depName=aquasecurity/trivy
readonly TRIVY_VERSION="v0.76.1"
# Archive hashes are independently pinned here rather than downloaded beside
# the archive. Source: the upstream v0.76.1 release checksums asset.
readonly TRIVY_SHA256_LINUX_AMD64="c7cbe3137fb8a48a17574bede3dd6beb16742d1e9772929940fc90082c39d9c0"
readonly TRIVY_SHA256_LINUX_ARM64="e174dff2a6dba5e3ae41f8d0537a6324405bd08132497340304c6b20e0f16a1f"
readonly TRIVY_SHA256_DARWIN_AMD64="7aa762eacd582052d053b62dcf972a1ab058abe39a57ccae234ed8894945b6b7"
readonly TRIVY_SHA256_DARWIN_ARM64="11a9b6cd2752011b54f01ea4bd0dbfa9f0c41dc6538d5a3f9d23955f471bf4e5"

install_trivy() {
  ci_use_installed_pin trivy Trivy "$TRIVY_VERSION" --version && return 0

  local install_dir raw_os raw_arch platform os arch asset sha256 archive version
  install_dir="$(ci_install_dir trivy)"
  mkdir -p "$install_dir"
  version="${TRIVY_VERSION#v}"
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  platform="$(ci_normalize_os "$raw_os")/$(ci_normalize_arch "$raw_arch")" || true

  case "$platform" in
  linux/amd64)
    os=Linux
    arch=64bit
    sha256="$TRIVY_SHA256_LINUX_AMD64"
    ;;
  linux/arm64)
    os=Linux
    arch=ARM64
    sha256="$TRIVY_SHA256_LINUX_ARM64"
    ;;
  darwin/amd64)
    os=macOS
    arch=64bit
    sha256="$TRIVY_SHA256_DARWIN_AMD64"
    ;;
  darwin/arm64)
    os=macOS
    arch=ARM64
    sha256="$TRIVY_SHA256_DARWIN_ARM64"
    ;;
  *)
    printf 'ERROR: unsupported platform for Trivy: %s/%s\n' "$raw_os" "$raw_arch" >&2
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
