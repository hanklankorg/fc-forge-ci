#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the pinned the publiccode-parser-go CLI, unless that version is already on PATH.
#
# Usage: source this file, then call install_publiccode_parser
#   source "$(dirname "$0")/../bootstrap/install-publiccode-parser.sh"
#   install_publiccode_parser

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=github-releases depName=italia/publiccode-parser-go
readonly PUBLICCODE_PARSER_VERSION="v5.4.3"
readonly PUBLICCODE_PARSER_SHA256_LINUX_AMD64="211847305611417ac0b76af5169086232ea6c75ff6ae686227b6972a41599dd7"
readonly PUBLICCODE_PARSER_SHA256_LINUX_ARM64="33363da55ddf38457f6f9081cc8f3b833e833eb3a401d6c9c3feb8e5f2dc3fd7"
readonly PUBLICCODE_PARSER_SHA256_DARWIN_AMD64="38d6bb4be977a1b0ea723e1c4decece852d3c143d15ba98620956e8617354d1f"
readonly PUBLICCODE_PARSER_SHA256_DARWIN_ARM64="61b3754455bfa5c04fe766d549e8b1d01fe804f7d914c93ee19e3ea28e6a46ae"

resolve_publiccode_parser_dist() {
  local raw_os raw_arch os="" arch="" dist=""
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  os="$(ci_normalize_os "$raw_os")" || true
  arch="$(ci_normalize_arch "$raw_arch")" || true

  case "$os/$arch" in
  linux/amd64) dist="publiccode-parser-go_Linux_x86_64.tar.gz" ;;
  linux/arm64) dist="publiccode-parser-go_Linux_arm64.tar.gz" ;;
  darwin/amd64) dist="publiccode-parser-go_Darwin_x86_64.tar.gz" ;;
  darwin/arm64) dist="publiccode-parser-go_Darwin_arm64.tar.gz" ;;
  esac

  if [[ -z "$dist" ]]; then
    printf "ERROR: Unsupported publiccode-parser platform: %s/%s\n" "$raw_os" "$raw_arch" >&2
    return 1
  fi

  printf '%s' "$dist"
}

resolve_publiccode_parser_sha256() {
  case "$1" in
  publiccode-parser-go_Linux_x86_64.tar.gz) printf '%s' "$PUBLICCODE_PARSER_SHA256_LINUX_AMD64" ;;
  publiccode-parser-go_Linux_arm64.tar.gz) printf '%s' "$PUBLICCODE_PARSER_SHA256_LINUX_ARM64" ;;
  publiccode-parser-go_Darwin_x86_64.tar.gz) printf '%s' "$PUBLICCODE_PARSER_SHA256_DARWIN_AMD64" ;;
  publiccode-parser-go_Darwin_arm64.tar.gz) printf '%s' "$PUBLICCODE_PARSER_SHA256_DARWIN_ARM64" ;;
  *)
    printf 'ERROR: no pinned SHA-256 for publiccode-parser asset %s\n' "$1" >&2
    return 1
    ;;
  esac
}

install_publiccode_parser() {
  ci_use_installed_pin publiccode-parser publiccode-parser "$PUBLICCODE_PARSER_VERSION" --version && return 0

  local install_dir dist sha256 asset_url archive
  install_dir="$(ci_install_dir publiccode-parser)"
  mkdir -p "$install_dir"

  dist="$(resolve_publiccode_parser_dist)" || return 1
  sha256="$(resolve_publiccode_parser_sha256 "$dist")" || return 1
  asset_url="https://github.com/italia/publiccode-parser-go/releases/download/${PUBLICCODE_PARSER_VERSION}/${dist}"
  archive="$install_dir/${dist}"

  printf "Installing publiccode-parser (version: %s)...\n" "$PUBLICCODE_PARSER_VERSION"
  if ! ci_download_verified "$asset_url" "$archive" "$sha256"; then
    printf "ERROR: Failed to download publiccode-parser from %s\n" "$asset_url" >&2
    return 1
  fi

  tar -xzf "$archive" -C "$install_dir"
  rm -f "$archive"
  chmod +x "$install_dir/publiccode-parser"
  ci_prepend_path "$install_dir"

  ci_require_command publiccode-parser publiccode-parser || return 1
  ci_print_installed "publiccode-parser" "$(publiccode-parser --version 2>/dev/null | head -1 | tr -d '\r')"
}
