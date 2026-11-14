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
readonly SYFT_VERSION="v1.57.0"
# Archive hashes are independently pinned here rather than downloaded beside
# the archive. Source: the upstream v1.57.0 release checksums asset.
readonly SYFT_SHA256_LINUX_AMD64="9c077a7d43029b3b13e98a8db720661e700c4d58db6352153a06395ae4a2a704"
readonly SYFT_SHA256_LINUX_ARM64="410cbfa0ae0f63246bc0057843760b1de2833cd23919e025e368fd7681308cdb"
readonly SYFT_SHA256_DARWIN_AMD64="f0e8568a9429ac1c8e4ae9e9c399fede5fcd09e4a7348b47b0532baf1c6500c1"
readonly SYFT_SHA256_DARWIN_ARM64="17012a5a110323bb1069e513a8665f1ae445905c56b4e3f9a9df4f95c5bb6161"

install_syft() {
  local install_dir raw_os raw_arch platform os arch asset sha256 archive version
  install_dir="$(ci_install_dir syft)"
  if [[ -L "$install_dir" || (-e "$install_dir" && ! -d "$install_dir") ]]; then
    printf 'ERROR: Syft install directory is not a real directory: %s\n' "$install_dir" >&2
    return 1
  fi
  mkdir -p "$install_dir"
  chmod 0700 "$install_dir"
  version="${SYFT_VERSION#v}"
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  platform="$(ci_normalize_os "$raw_os")/$(ci_normalize_arch "$raw_arch")" || true

  case "$platform" in
  linux/amd64)
    os=linux
    arch=amd64
    sha256="$SYFT_SHA256_LINUX_AMD64"
    ;;
  linux/arm64)
    os=linux
    arch=arm64
    sha256="$SYFT_SHA256_LINUX_ARM64"
    ;;
  darwin/amd64)
    os=darwin
    arch=amd64
    sha256="$SYFT_SHA256_DARWIN_AMD64"
    ;;
  darwin/arm64)
    os=darwin
    arch=arm64
    sha256="$SYFT_SHA256_DARWIN_ARM64"
    ;;
  *)
    printf 'ERROR: unsupported platform for Syft: %s/%s\n' "$raw_os" "$raw_arch" >&2
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
