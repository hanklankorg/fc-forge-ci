#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the pinned GitLab CLI (glab), unless that version is already on PATH.
#
# Usage: source this file, then call install_glab
#   source "$(dirname "$0")/../bootstrap/install-glab.sh"
#   install_glab

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=gitlab-releases depName=gitlab-org/cli
readonly GLAB_VERSION="v1.124.0"
readonly GLAB_SHA256_LINUX_AMD64="63df684842b0697e418649dd64374d37ac1da0fbcdcb422d6a5557222f8bb40f"
readonly GLAB_SHA256_LINUX_ARM64="0716395a0efeb991fc844b33fe55310645afcbd9b9746e50c725e9d1cd1b8d16"
readonly GLAB_SHA256_DARWIN_AMD64="6adb7155566bedd6272d8f286e6c75b080d0050a349de4bed79661226d10d954"
readonly GLAB_SHA256_DARWIN_ARM64="04aad4d989d98cb030318e09359f02e0ba2637ede682ec0dee1f5974dedd44c1"

resolve_glab_dist() {
  local raw_os raw_arch os="" arch="" dist=""
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  os="$(ci_normalize_os "$raw_os")" || true
  arch="$(ci_normalize_arch "$raw_arch")" || true

  case "$os/$arch" in
  linux/amd64) dist="glab_${GLAB_VERSION#v}_linux_amd64.tar.gz" ;;
  linux/arm64) dist="glab_${GLAB_VERSION#v}_linux_arm64.tar.gz" ;;
  darwin/amd64) dist="glab_${GLAB_VERSION#v}_darwin_amd64.tar.gz" ;;
  darwin/arm64) dist="glab_${GLAB_VERSION#v}_darwin_arm64.tar.gz" ;;
  esac

  if [[ -z "$dist" ]]; then
    printf "ERROR: Unsupported glab platform: %s/%s\n" "$raw_os" "$raw_arch" >&2
    return 1
  fi

  printf '%s' "$dist"
}

resolve_glab_sha256() {
  case "$1" in
  glab_1.124.0_linux_amd64.tar.gz) printf '%s' "$GLAB_SHA256_LINUX_AMD64" ;;
  glab_1.124.0_linux_arm64.tar.gz) printf '%s' "$GLAB_SHA256_LINUX_ARM64" ;;
  glab_1.124.0_darwin_amd64.tar.gz) printf '%s' "$GLAB_SHA256_DARWIN_AMD64" ;;
  glab_1.124.0_darwin_arm64.tar.gz) printf '%s' "$GLAB_SHA256_DARWIN_ARM64" ;;
  *)
    printf 'ERROR: no pinned SHA-256 for glab asset %s\n' "$1" >&2
    return 1
    ;;
  esac
}

install_glab() {
  ci_use_installed_pin glab glab "$GLAB_VERSION" --version && return 0

  local install_dir dist sha256 asset_url archive
  install_dir="$(ci_install_dir glab)"
  mkdir -p "$install_dir"

  dist="$(resolve_glab_dist)" || return 1
  sha256="$(resolve_glab_sha256 "$dist")" || return 1
  asset_url="https://gitlab.com/gitlab-org/cli/-/releases/${GLAB_VERSION}/downloads/${dist}"
  archive="$install_dir/${dist}"

  printf "Installing glab (version: %s)...\n" "$GLAB_VERSION"
  if ! ci_download_verified "$asset_url" "$archive" "$sha256"; then
    printf "ERROR: Failed to download glab from %s\n" "$asset_url" >&2
    return 1
  fi

  tar -xzf "$archive" -C "$install_dir" --strip-components=1
  rm -f "$archive"
  chmod +x "$install_dir/bin/glab" 2>/dev/null || chmod +x "$install_dir/glab"
  # glab tarball variants put binary at bin/glab or directly at root; symlink
  # to the canonical path either way.
  if [[ -x "$install_dir/bin/glab" ]]; then
    mv "$install_dir/bin/glab" "$install_dir/glab"
    rm -rf "${install_dir:?}/bin"
  fi
  ci_prepend_path "$install_dir"

  ci_require_command glab glab || return 1
  ci_print_installed "glab" "$(glab --version 2>/dev/null | head -1 | tr -d '\r')"
}
