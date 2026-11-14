#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
# Shared helpers (install directory, OS and architecture normalization),
# sourced by the other bootstrap installers; not run on its own.

ci_install_dir() {
  local tool="$1"
  printf '%s/%s-bin' "${CI_TEMP_DIR:-/tmp}" "$tool"
}

# Normalize the uname and Go spellings accepted by the bootstrap installers.
# Asset-specific names (macOS, x64, triples, and so on) stay in each installer.
ci_normalize_os() {
  case "$1" in
  Linux | linux) printf 'linux' ;;
  Darwin | darwin) printf 'darwin' ;;
  *) return 1 ;;
  esac
}

ci_normalize_arch() {
  case "$1" in
  x86_64 | amd64) printf 'amd64' ;;
  aarch64 | arm64) printf 'arm64' ;;
  armv6l | armv7l | arm) printf 'arm' ;;
  *) return 1 ;;
  esac
}

ci_verify_sha256() {
  local file="$1"
  local expected="$2"
  local actual

  if command -v sha256sum &>/dev/null; then
    actual="$(sha256sum "$file" | cut -d' ' -f1)"
  elif command -v shasum &>/dev/null; then
    actual="$(shasum -a 256 "$file" | cut -d' ' -f1)"
  else
    printf 'ERROR: neither sha256sum nor shasum is available\n' >&2
    rm -f "$file"
    return 1
  fi

  if [[ "$actual" != "$expected" ]]; then
    printf 'ERROR: SHA-256 mismatch for %s (expected %s, got %s)\n' "$file" "$expected" "$actual" >&2
    rm -f "$file"
    return 1
  fi
}

# curl owns transient-error classification, Retry-After and exponential backoff.
# The retry window bounds starting new attempts; each attempt is also bounded.
ci_download() {
  local url="$1" destination="$2"
  printf 'Downloading %s\n' "$url" >&2
  if ! curl --retry 5 --retry-max-time 600 --connect-timeout 30 --max-time 180 \
    --retry-connrefused --proto '=https' --proto-redir '=https' --tlsv1.2 \
    -sSfL "$url" -o "$destination"; then
    rm -f "$destination"
    printf 'ERROR: download failed: %s\n' "$url" >&2
    return 1
  fi
}

ci_download_verified() {
  local url="$1"
  local destination="$2"
  local sha256="$3"
  if [[ ! "$sha256" =~ ^[0-9a-f]{64}$ ]]; then
    printf 'ERROR: a pinned SHA-256 is required before downloading %s\n' "$url" >&2
    return 1
  fi

  ci_download "$url" "$destination" || return 1
  ci_verify_sha256 "$destination" "$sha256"
}

# ci_prepend_path puts dir first on PATH for this shell and, on GitHub and
# Forgejo ($GITHUB_PATH, $FORGEJO_PATH), for the job's later steps: a step's
# exported PATH ends with the step, so a tool installed in one step and run in
# the next (trivy before the image scan) was otherwise "not found". An install
# into a directory that goes away clears both variables for the call.
ci_prepend_path() {
  local dir="$1"
  export PATH="${dir}:$PATH"
  # Forgejo's own name first, as setup-forge-ci.sh reads it.
  local path_file=${FORGEJO_PATH:-${GITHUB_PATH:-}}
  if [[ -n "$path_file" ]]; then
    printf '%s\n' "$dir" >>"$path_file"
  fi
}

ci_require_command() {
  local cmd="$1"
  local label="$2"
  if ! command -v "$cmd" &>/dev/null; then
    printf 'ERROR: %s binary not found after install\n' "$label" >&2
    return 1
  fi
}

# ci_use_installed_pin succeeds when cmd on PATH already is the pinned
# version: its version output names the pin as a whole version, with or
# without a leading v. Anything else, an unreadable version or a mise shim
# with nothing behind it included, is left for the pinned, checksum-verified
# download to replace, because the jobs' results depend on the exact version.
#   ci_use_installed_pin trivy Trivy "$TRIVY_VERSION" --version && return 0
ci_use_installed_pin() {
  local cmd="$1" label="$2" pin="$3" version reported
  shift 3
  command -v "$cmd" &>/dev/null || return 1

  version="${pin#v}"
  reported="$("$cmd" "$@" 2>/dev/null | tr -d '\r')" || true
  if grep -Eq "(^|[^0-9A-Za-z.])v?${version//./\\.}([^0-9A-Za-z.+-]|\$)" <<<"$reported"; then
    printf '%s already installed: %s\n\n' "$label" "$(head -1 <<<"$reported")"
    return 0
  fi
  printf '%s on PATH is not the pinned %s (it reports: %s); installing the pinned one\n' \
    "$label" "$pin" "$(head -1 <<<"${reported:-nothing}")"
  return 1
}

ci_print_installed() {
  local label="$1"
  local version="$2"
  printf '%s installed successfully: %s\n\n' "$label" "$version"
}
