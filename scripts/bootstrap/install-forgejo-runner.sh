#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install the repository-pinned forgejo-runner.
#
# A lent machine runs it for one job: scripts/runner-lender/lend-forgejo-runner.sh
# registers the runner with the orchestrating forge and waits for that job.
# Linux only; the runner is lent to a Linux job.
#
# Usage: source this file, then call install_forgejo_runner
#   source "$(dirname "$0")/../bootstrap/install-forgejo-runner.sh"
#   install_forgejo_runner

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"

# renovate: datasource=forgejo-releases depName=forgejo/runner registryUrl=https://code.forgejo.org
readonly FORGEJO_RUNNER_VERSION="v13.2.1"
readonly FORGEJO_RUNNER_SHA256_LINUX_AMD64="bb369fe1ecb50fdc983c0b1d993f19b0920df6e5864511892b3cdbf16d858b34"
readonly FORGEJO_RUNNER_SHA256_LINUX_ARM64="eeddb7902f79abe488972f4189fbe3ea02762ee827d1b406b469420bf0d62dd9"

resolve_forgejo_runner_asset() {
  local raw_os raw_arch os="" arch="" asset=""
  raw_os="${OS:-$(uname -s)}"
  raw_arch="${ARCH:-$(uname -m)}"
  os="$(ci_normalize_os "$raw_os")" || true
  arch="$(ci_normalize_arch "$raw_arch")" || true

  case "$os/$arch" in
  linux/amd64 | linux/arm64) asset="forgejo-runner-${FORGEJO_RUNNER_VERSION#v}-linux-${arch}" ;;
  esac

  if [[ -z "$asset" ]]; then
    printf 'ERROR: unsupported forgejo-runner platform: %s/%s\n' "$raw_os" "$raw_arch" >&2
    return 1
  fi

  printf '%s' "$asset"
}

resolve_forgejo_runner_sha256() {
  case "$1" in
  forgejo-runner-*-linux-amd64) printf '%s' "$FORGEJO_RUNNER_SHA256_LINUX_AMD64" ;;
  forgejo-runner-*-linux-arm64) printf '%s' "$FORGEJO_RUNNER_SHA256_LINUX_ARM64" ;;
  *)
    printf 'ERROR: no pinned SHA-256 for forgejo-runner asset %s\n' "$1" >&2
    return 1
    ;;
  esac
}

install_forgejo_runner() {
  local install_dir asset sha256 url download

  install_dir="$(ci_install_dir forgejo-runner)"
  if [[ -L "$install_dir" || (-e "$install_dir" && ! -d "$install_dir") ]]; then
    printf 'ERROR: forgejo-runner install directory is not a real directory: %s\n' "$install_dir" >&2
    return 1
  fi
  mkdir -p "$install_dir"
  chmod 0755 "$install_dir"

  asset="$(resolve_forgejo_runner_asset)" || return 1
  sha256="$(resolve_forgejo_runner_sha256 "$asset")" || return 1

  url="https://code.forgejo.org/forgejo/runner/releases/download/${FORGEJO_RUNNER_VERSION}/${asset}"
  download="$(mktemp "$install_dir/.forgejo-runner-download.XXXXXXXX")"

  printf "Installing forgejo-runner (version: %s)...\n" "${FORGEJO_RUNNER_VERSION}"
  if ! ci_download_verified "$url" "$download" "$sha256"; then
    printf 'ERROR: Failed to download forgejo-runner from %s\n' "$url" >&2
    rm -f "$download"
    return 1
  fi

  # The lender runs the binary as another, unprivileged user: world-readable
  # and executable, like a packaged binary, and owned by whoever installs.
  install -m 0755 "$download" "$install_dir/forgejo-runner"
  rm -f "$download"

  # The checksum pins the exact release; no version probe is needed.
  ci_prepend_path "$install_dir"
  ci_require_command forgejo-runner forgejo-runner || return 1
  ci_print_installed forgejo-runner "$FORGEJO_RUNNER_VERSION"
}
