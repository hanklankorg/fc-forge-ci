#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the pinned git-cliff, unless that version is already on PATH.
#
# Usage: source this file, then call install_git_cliff
#   source "$(dirname "$0")/../bootstrap/install-git-cliff.sh"
#   install_git_cliff

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=github-releases depName=orhun/git-cliff
readonly GIT_CLIFF_VERSION="v2.14.2"
readonly GIT_CLIFF_SHA256_LINUX_AMD64="24f397c733add5390fdceee3a2088588ab0d5f944ce00d34cb7029b888cf2db4"
readonly GIT_CLIFF_SHA256_LINUX_ARM64="69e208f6e4baef3a0f9b759d5f44223aa9336b86c691072a3d0c98d35a816e69"
readonly GIT_CLIFF_SHA256_DARWIN_AMD64="4e647d2deb152aca1e987f225cf42b080b991dd98f3bce4027ef95446d982ac3"
readonly GIT_CLIFF_SHA256_DARWIN_ARM64="a0be97e237d440d34c5e508ec21d070b2c85cbb8be48a46a56f9a4e8bd247f05"

resolve_git_cliff_dist() {
  local raw_os raw_arch os="" arch="" dist=""
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  os="$(ci_normalize_os "$raw_os")" || true
  arch="$(ci_normalize_arch "$raw_arch")" || true

  case "$os/$arch" in
  linux/amd64) dist="git-cliff-${GIT_CLIFF_VERSION#v}-x86_64-unknown-linux-gnu.tar.gz" ;;
  linux/arm64) dist="git-cliff-${GIT_CLIFF_VERSION#v}-aarch64-unknown-linux-gnu.tar.gz" ;;
  darwin/amd64) dist="git-cliff-${GIT_CLIFF_VERSION#v}-x86_64-apple-darwin.tar.gz" ;;
  darwin/arm64) dist="git-cliff-${GIT_CLIFF_VERSION#v}-aarch64-apple-darwin.tar.gz" ;;
  esac

  if [[ -z "$dist" ]]; then
    printf "ERROR: Unsupported git-cliff platform: %s/%s\n" "$raw_os" "$raw_arch" >&2
    return 1
  fi

  printf '%s' "$dist"
}

resolve_git_cliff_sha256() {
  case "$1" in
  git-cliff-2.14.2-x86_64-unknown-linux-gnu.tar.gz) printf '%s' "$GIT_CLIFF_SHA256_LINUX_AMD64" ;;
  git-cliff-2.14.2-aarch64-unknown-linux-gnu.tar.gz) printf '%s' "$GIT_CLIFF_SHA256_LINUX_ARM64" ;;
  git-cliff-2.14.2-x86_64-apple-darwin.tar.gz) printf '%s' "$GIT_CLIFF_SHA256_DARWIN_AMD64" ;;
  git-cliff-2.14.2-aarch64-apple-darwin.tar.gz) printf '%s' "$GIT_CLIFF_SHA256_DARWIN_ARM64" ;;
  *)
    printf 'ERROR: no pinned SHA-256 for git-cliff asset %s\n' "$1" >&2
    return 1
    ;;
  esac
}

install_git_cliff() {
  ci_use_installed_pin git-cliff git-cliff "$GIT_CLIFF_VERSION" --version && return 0

  local install_dir dist sha256 asset_url archive
  install_dir="$(ci_install_dir git-cliff)"
  mkdir -p "$install_dir"

  dist="$(resolve_git_cliff_dist)" || return 1
  sha256="$(resolve_git_cliff_sha256 "$dist")" || return 1
  asset_url="https://github.com/orhun/git-cliff/releases/download/${GIT_CLIFF_VERSION}/${dist}"
  archive="$install_dir/${dist}"

  printf "Installing git-cliff (version: %s)...\n" "$GIT_CLIFF_VERSION"
  if ! ci_download_verified "$asset_url" "$archive" "$sha256"; then
    printf "ERROR: Failed to download git-cliff from %s\n" "$asset_url" >&2
    return 1
  fi

  tar -xzf "$archive" -C "$install_dir" --strip-components=1 || return 1
  rm -f "$archive"
  chmod +x "$install_dir/git-cliff"
  ci_prepend_path "$install_dir"

  ci_require_command git-cliff git-cliff || return 1
  ci_print_installed "git-cliff" "$(git-cliff --version 2>/dev/null | tr -d '\r')"
}
