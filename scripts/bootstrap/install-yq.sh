#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the pinned Mike Farah yq, unless that version is already on PATH.
#
# Usage: source this file, then call install_yq
#   source "$(dirname "$0")/../bootstrap/install-yq.sh"
#   install_yq

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=github-releases depName=mikefarah/yq
readonly YQ_VERSION="v4.54.1"
readonly YQ_SHA256_LINUX_AMD64="8e34fc298390875de416e6a4afcb8cabeceb25d9aa8506c1a2f9353cf702ea5f"
readonly YQ_SHA256_LINUX_ARM64="189088da0c6429ec5178dfaab1a114805f6cab0b61b165ab236efedf1d57a71b"
readonly YQ_SHA256_LINUX_ARM="a5d45c07b75fab772193c4a79f4df9f5c8ed33836fa0f36dcc947f9495439b00"
readonly YQ_SHA256_DARWIN_AMD64="3a812fce205a4d67014fb71b7fa297092210e580df3d8f52dddc2cf3a599c22b"
readonly YQ_SHA256_DARWIN_ARM64="fee511a181bd8b3e6b7da98842b41973bfac8cd3bcd341ed29a584620b5b2844"

resolve_yq_dist() {
  local raw_os raw_arch os="" arch="" dist=""
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  os="$(ci_normalize_os "$raw_os")" || true
  arch="$(ci_normalize_arch "$raw_arch")" || true

  case "$os/$arch" in
  linux/amd64) dist="yq_linux_amd64" ;;
  linux/arm64) dist="yq_linux_arm64" ;;
  linux/arm) dist="yq_linux_arm" ;;
  darwin/amd64) dist="yq_darwin_amd64" ;;
  darwin/arm64) dist="yq_darwin_arm64" ;;
  esac

  if [[ -z "$dist" ]]; then
    printf "ERROR: Unsupported yq platform: %s/%s\n" "$raw_os" "$raw_arch" >&2
    return 1
  fi

  printf '%s' "$dist"
}

resolve_yq_sha256() {
  case "$1" in
  yq_linux_amd64) printf '%s' "$YQ_SHA256_LINUX_AMD64" ;;
  yq_linux_arm64) printf '%s' "$YQ_SHA256_LINUX_ARM64" ;;
  yq_linux_arm) printf '%s' "$YQ_SHA256_LINUX_ARM" ;;
  yq_darwin_amd64) printf '%s' "$YQ_SHA256_DARWIN_AMD64" ;;
  yq_darwin_arm64) printf '%s' "$YQ_SHA256_DARWIN_ARM64" ;;
  *)
    printf 'ERROR: no pinned SHA-256 for yq asset %s\n' "$1" >&2
    return 1
    ;;
  esac
}

install_yq() {
  ci_use_installed_pin yq yq "$YQ_VERSION" --version && return 0

  local install_dir dist sha256 asset_url download
  install_dir="$(ci_install_dir yq)"
  mkdir -p "$install_dir"

  dist="$(resolve_yq_dist)" || return 1
  sha256="$(resolve_yq_sha256 "$dist")" || return 1
  asset_url="https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/${dist}"
  download="$(mktemp "$install_dir/.yq-download.XXXXXXXX")"

  printf "Installing yq (version: %s)...\n" "$YQ_VERSION"
  if ! ci_download_verified "$asset_url" "$download" "$sha256"; then
    rm -f "$download"
    printf "ERROR: Failed to download yq from %s\n" "$asset_url" >&2
    return 1
  fi

  install -m 0755 "$download" "$install_dir/yq"
  rm -f "$download"
  ci_prepend_path "$install_dir"

  ci_require_command yq yq || return 1
  ci_print_installed "yq" "$(yq --version 2>/dev/null | tr -d '\r')"
}
