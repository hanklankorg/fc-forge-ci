#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the pinned cyclonedx-gomod, the Go Build SBOM generator.
#
# forge-ci's runtime image carries it; a Forgejo build job runs on the
# consumer's runner instead, so build-go brings it the same way release-images
# brings Trivy. A cyclonedx-gomod already on PATH (from the consumer's mise
# config, say) is used only when it is the pinned version.
#
# Usage: source this file, then call install_cyclonedx_gomod
#   source "$(dirname "$0")/../bootstrap/install-cyclonedx-gomod.sh"
#   install_cyclonedx_gomod

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# Keep in step with CYCLONEDX_GOMOD_VERSION in containers/runtime/Containerfile.
# renovate: datasource=github-releases depName=CycloneDX/cyclonedx-gomod
readonly CYCLONEDX_GOMOD_VERSION="v1.12.0"
# Archive hashes are independently pinned here rather than downloaded beside
# the archive. Source: the upstream v1.12.0 release checksums asset.
readonly CYCLONEDX_GOMOD_SHA256_LINUX_AMD64="004b9f5cc595b797fb5423e2ae4c97bcf0f18c712ed2faee1640b09e5efd6d15"
readonly CYCLONEDX_GOMOD_SHA256_LINUX_ARM64="b6dd6424755e61c0f7fcd36074fbb78798a33c52f1bd35dd177ba62eccadf404"
readonly CYCLONEDX_GOMOD_SHA256_DARWIN_AMD64="94ab2d999341e4bc8d26767bffda2823ac4507c2f17c01af6d3738f5341b9e85"
readonly CYCLONEDX_GOMOD_SHA256_DARWIN_ARM64="43dcd58b7a7ef9d84a2d21df9c6eeeb907d5b4ee2b1fe6c1330e5bc9b478281e"

install_cyclonedx_gomod() {
  ci_use_installed_pin cyclonedx-gomod cyclonedx-gomod "$CYCLONEDX_GOMOD_VERSION" version && return 0

  local install_dir raw_os raw_arch platform os arch asset sha256 archive version
  install_dir="$(ci_install_dir cyclonedx-gomod)"
  mkdir -p "$install_dir"
  version="${CYCLONEDX_GOMOD_VERSION#v}"
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  platform="$(ci_normalize_os "$raw_os")/$(ci_normalize_arch "$raw_arch")" || true

  case "$platform" in
  linux/amd64)
    os=linux
    arch=amd64
    sha256="$CYCLONEDX_GOMOD_SHA256_LINUX_AMD64"
    ;;
  linux/arm64)
    os=linux
    arch=arm64
    sha256="$CYCLONEDX_GOMOD_SHA256_LINUX_ARM64"
    ;;
  darwin/amd64)
    os=darwin
    arch=amd64
    sha256="$CYCLONEDX_GOMOD_SHA256_DARWIN_AMD64"
    ;;
  darwin/arm64)
    os=darwin
    arch=arm64
    sha256="$CYCLONEDX_GOMOD_SHA256_DARWIN_ARM64"
    ;;
  *)
    printf 'ERROR: unsupported platform for cyclonedx-gomod: %s/%s\n' "$raw_os" "$raw_arch" >&2
    return 1
    ;;
  esac
  asset="cyclonedx-gomod_${version}_${os}_${arch}.tar.gz"
  archive="$install_dir/$asset"

  printf "Installing cyclonedx-gomod (version: %s)...\n" "${CYCLONEDX_GOMOD_VERSION}"
  if ! ci_download_verified "https://github.com/CycloneDX/cyclonedx-gomod/releases/download/${CYCLONEDX_GOMOD_VERSION}/${asset}" "$archive" "$sha256" ||
    ! tar -xzf "$archive" -C "$install_dir" cyclonedx-gomod; then
    rm -f "$archive" "$install_dir/cyclonedx-gomod"
    printf "ERROR: Failed to install cyclonedx-gomod %s\n" "${CYCLONEDX_GOMOD_VERSION}" >&2
    return 1
  fi
  rm -f "$archive"

  ci_prepend_path "$install_dir"
  ci_require_command cyclonedx-gomod cyclonedx-gomod || return 1
  ci_print_installed "cyclonedx-gomod" "$(cyclonedx-gomod version 2>/dev/null | head -1)"
}
