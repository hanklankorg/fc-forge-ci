#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the pinned cargo-cyclonedx, the Cargo Build SBOM generator.
#
# forge-ci's runtime image carries it; a Forgejo build job runs on the
# consumer's runner instead, so build-cargo brings it the same way
# release-images brings Trivy. A cargo-cyclonedx already on PATH (from the
# consumer's mise config, say) is used only when it is the pinned version.
#
# Usage: source this file, then call install_cargo_cyclonedx
#   source "$(dirname "$0")/../bootstrap/install-cargo-cyclonedx.sh"
#   install_cargo_cyclonedx

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# Keep in step with CARGO_CYCLONEDX_VERSION in containers/runtime/Containerfile.
# renovate: datasource=github-releases depName=CycloneDX/cyclonedx-rust-cargo extractVersion=^cargo-cyclonedx-(?<version>.+)$
readonly CARGO_CYCLONEDX_VERSION="0.5.9"
# Archive hashes are independently pinned here rather than downloaded beside
# the archive. Source: the upstream cargo-cyclonedx-0.5.9 sha256.sum asset.
readonly CARGO_CYCLONEDX_SHA256_LINUX_AMD64="fb8dbee9f182173e062a64a387b21a0badc6fab8b2abf9294973f012972bf6d8"
readonly CARGO_CYCLONEDX_SHA256_LINUX_ARM64="7bf131ca5389b07a4f10c182bcf8a5ad339d64408b6f0d8f6834a0bd6120a06a"
readonly CARGO_CYCLONEDX_SHA256_DARWIN_AMD64="59d2a583fa632f8759456c1b531340331255b277386d23c598a3dbbc916fde63"
readonly CARGO_CYCLONEDX_SHA256_DARWIN_ARM64="4c53dfa21e70b65bf7f8d2592aadde3bcb02c1a40b6ec63b877e5ca65a29e180"

install_cargo_cyclonedx() {
  ci_use_installed_pin cargo-cyclonedx cargo-cyclonedx "$CARGO_CYCLONEDX_VERSION" cyclonedx --version && return 0

  local install_dir raw_os raw_arch platform triple asset sha256 archive
  install_dir="$(ci_install_dir cargo-cyclonedx)"
  mkdir -p "$install_dir"
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  platform="$(ci_normalize_os "$raw_os")/$(ci_normalize_arch "$raw_arch")" || true

  case "$platform" in
  linux/amd64)
    triple=x86_64-unknown-linux-gnu
    sha256="$CARGO_CYCLONEDX_SHA256_LINUX_AMD64"
    ;;
  linux/arm64)
    triple=aarch64-unknown-linux-gnu
    sha256="$CARGO_CYCLONEDX_SHA256_LINUX_ARM64"
    ;;
  darwin/amd64)
    triple=x86_64-apple-darwin
    sha256="$CARGO_CYCLONEDX_SHA256_DARWIN_AMD64"
    ;;
  darwin/arm64)
    triple=aarch64-apple-darwin
    sha256="$CARGO_CYCLONEDX_SHA256_DARWIN_ARM64"
    ;;
  *)
    printf 'ERROR: unsupported platform for cargo-cyclonedx: %s/%s\n' "$raw_os" "$raw_arch" >&2
    return 1
    ;;
  esac
  asset="cargo-cyclonedx-${triple}.tar.xz"
  archive="$install_dir/$asset"

  printf "Installing cargo-cyclonedx (version: %s)...\n" "${CARGO_CYCLONEDX_VERSION}"
  # tar -xf detects the compression; upstream ships xz, which needs xz on PATH.
  if ! ci_download_verified "https://github.com/CycloneDX/cyclonedx-rust-cargo/releases/download/cargo-cyclonedx-${CARGO_CYCLONEDX_VERSION}/${asset}" "$archive" "$sha256" ||
    ! tar -xf "$archive" -C "$install_dir" --strip-components=1 "cargo-cyclonedx-${triple}/cargo-cyclonedx"; then
    rm -f "$archive" "$install_dir/cargo-cyclonedx"
    printf "ERROR: Failed to install cargo-cyclonedx %s (the archive is .tar.xz: the runner needs xz)\n" "${CARGO_CYCLONEDX_VERSION}" >&2
    return 1
  fi
  rm -f "$archive"

  ci_prepend_path "$install_dir"
  ci_require_command cargo-cyclonedx cargo-cyclonedx || return 1
  ci_print_installed "cargo-cyclonedx" "$(cargo-cyclonedx cyclonedx --version 2>/dev/null | head -1)"
}
