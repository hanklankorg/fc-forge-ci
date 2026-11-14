#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the pinned mise (dev tool version manager), unless that version is already on PATH.
#
# Usage: source this file, then call install_mise
#   source "$(dirname "$0")/../bootstrap/install-mise.sh"
#   install_mise

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=github-releases depName=jdx/mise
readonly MISE_VERSION="v2026.10.19"
readonly MISE_SHA256_LINUX_AMD64="3162760f40b6b5f706c60641e4ef5ee7322bc6c54f72fc26a7111b337aa18d9d"
readonly MISE_SHA256_LINUX_ARM64="4a4ca53b28e5433c527f4f73fac1f98f992a17cf32ca136a58e2e375f790f627"
readonly MISE_SHA256_DARWIN_AMD64="8d5e1591811143f15334f690373cefb58c13bd6cbd0292008748fd777f6c99d8"
readonly MISE_SHA256_DARWIN_ARM64="7dc61dfa71abbcda1c40d18440bc556b5d58b80e6042bc4f95fd106fa1bb3c66"

resolve_mise_dist() {
  local raw_os raw_arch os="" arch="" dist=""
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  os="$(ci_normalize_os "$raw_os")" || true
  arch="$(ci_normalize_arch "$raw_arch")" || true

  case "$os/$arch" in
  linux/amd64) dist="mise-${MISE_VERSION}-linux-x64" ;;
  linux/arm64) dist="mise-${MISE_VERSION}-linux-arm64" ;;
  darwin/amd64) dist="mise-${MISE_VERSION}-macos-x64" ;;
  darwin/arm64) dist="mise-${MISE_VERSION}-macos-arm64" ;;
  esac

  if [[ -z "$dist" ]]; then
    printf "ERROR: Unsupported mise platform: %s/%s\n" "$raw_os" "$raw_arch" >&2
    return 1
  fi

  printf '%s' "$dist"
}

resolve_mise_sha256() {
  case "$1" in
  mise-v2026.10.19-linux-x64) printf '%s' "$MISE_SHA256_LINUX_AMD64" ;;
  mise-v2026.10.19-linux-arm64) printf '%s' "$MISE_SHA256_LINUX_ARM64" ;;
  mise-v2026.10.19-macos-x64) printf '%s' "$MISE_SHA256_DARWIN_AMD64" ;;
  mise-v2026.10.19-macos-arm64) printf '%s' "$MISE_SHA256_DARWIN_ARM64" ;;
  *)
    printf 'ERROR: no pinned SHA-256 for mise asset %s\n' "$1" >&2
    return 1
    ;;
  esac
}

install_mise() {
  ci_use_installed_pin mise mise "$MISE_VERSION" --version && return 0

  local install_dir dist sha256 asset_url download
  install_dir="$(ci_install_dir mise)"
  mkdir -p "$install_dir"

  dist="$(resolve_mise_dist)" || return 1
  sha256="$(resolve_mise_sha256 "$dist")" || return 1
  asset_url="https://github.com/jdx/mise/releases/download/${MISE_VERSION}/${dist}"
  download="$(mktemp "$install_dir/.mise-download.XXXXXXXX")"

  printf "Installing mise (version: %s)...\n" "$MISE_VERSION"
  if ! ci_download_verified "$asset_url" "$download" "$sha256"; then
    rm -f "$download"
    printf "ERROR: Failed to download mise from %s\n" "$asset_url" >&2
    return 1
  fi

  install -m 0755 "$download" "$install_dir/mise"
  rm -f "$download"
  ci_prepend_path "$install_dir"

  ci_require_command mise mise || return 1
  ci_print_installed "mise" "$(mise --version 2>/dev/null | tr -d '\r')"
}
