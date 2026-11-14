#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the repository-pinned cosign (Sigstore signing CLI).
#
# cosign is required by forge-ci's Sigstore-keyless and KMS-backed
# signing paths (`release sign --method=sigstore|kms`). The GPG path
# does not use cosign.
#
# Usage: source this file, then call install_cosign
#   source "$(dirname "$0")/../bootstrap/install-cosign.sh"
#   install_cosign

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=github-releases depName=sigstore/cosign
# cosign 3.x is required: the v3 bundle format is the only output
# layout we support. Adapters pass --bundle / --new-bundle-format.
readonly COSIGN_VERSION="v3.1.4"
readonly COSIGN_SHA256_LINUX_AMD64="424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa"
readonly COSIGN_SHA256_LINUX_ARM64="ade83b7aac62e131cacfe96d958948b2150cc9efec46a570a2649b8e27dedad2"
readonly COSIGN_SHA256_DARWIN_AMD64="9d1e4abe2b6be279ed0d2ff706fde1ccd738747c834e3e6d44039c238867334f"
readonly COSIGN_SHA256_DARWIN_ARM64="9f9b8cd557cf2e5b3df3373a00baa005a86b02391e3e06dd0ce37b3a1533d3a4"

resolve_cosign_asset() {
  local raw_os raw_arch os="" arch="" asset=""
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  os="$(ci_normalize_os "$raw_os")" || true
  arch="$(ci_normalize_arch "$raw_arch")" || true

  case "$os/$arch" in
  linux/amd64 | linux/arm64) asset="cosign-linux-${arch}" ;;
  darwin/amd64 | darwin/arm64) asset="cosign-darwin-${arch}" ;;
  esac

  if [[ -z "$asset" ]]; then
    printf 'ERROR: unsupported cosign platform: %s/%s\n' "$raw_os" "$raw_arch" >&2
    return 1
  fi

  printf '%s' "$asset"
}

resolve_cosign_sha256() {
  case "$1" in
  cosign-linux-amd64) printf '%s' "$COSIGN_SHA256_LINUX_AMD64" ;;
  cosign-linux-arm64) printf '%s' "$COSIGN_SHA256_LINUX_ARM64" ;;
  cosign-darwin-amd64) printf '%s' "$COSIGN_SHA256_DARWIN_AMD64" ;;
  cosign-darwin-arm64) printf '%s' "$COSIGN_SHA256_DARWIN_ARM64" ;;
  *)
    printf 'ERROR: no pinned SHA-256 for cosign asset %s\n' "$1" >&2
    return 1
    ;;
  esac
}

install_cosign() {
  local install_dir asset sha256 url download

  install_dir="$(ci_install_dir cosign)"
  if [[ -L "$install_dir" || (-e "$install_dir" && ! -d "$install_dir") ]]; then
    printf 'ERROR: cosign install directory is not a real directory: %s\n' "$install_dir" >&2
    return 1
  fi
  mkdir -p "$install_dir"
  chmod 0700 "$install_dir"

  asset="$(resolve_cosign_asset)" || return 1
  sha256="$(resolve_cosign_sha256 "$asset")" || return 1

  url="https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/${asset}"
  download="$(mktemp "$install_dir/.cosign-download.XXXXXXXX")"

  printf "Installing cosign (version: %s)...\n" "${COSIGN_VERSION}"
  if ! ci_download_verified "$url" "$download" "$sha256"; then
    rm -f "$download"
    printf "ERROR: Failed to download cosign %s\n" "${COSIGN_VERSION}" >&2
    return 1
  fi

  rm -f "${install_dir}/cosign"
  install -m 0755 "$download" "${install_dir}/cosign"
  rm -f "$download"

  ci_prepend_path "$install_dir"
  ci_require_command cosign cosign || return 1
  if ! cosign version 2>/dev/null | grep -Eq "GitVersion:[[:space:]]*${COSIGN_VERSION}([[:space:]]|$)"; then
    printf 'ERROR: installed cosign does not report expected version %s\n' "$COSIGN_VERSION" >&2
    rm -f "${install_dir}/cosign"
    return 1
  fi
  ci_print_installed "cosign" "$(cosign version 2>/dev/null | grep -o 'GitVersion:[^ ]*' | head -1)"
}
