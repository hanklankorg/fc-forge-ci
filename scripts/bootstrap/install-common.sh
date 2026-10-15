#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

ci_install_dir() {
	local tool="$1"
	printf '%s/%s-bin' "${CI_TEMP_DIR:-/tmp}" "$tool"
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

ci_download_verified() {
	local url="$1"
	local destination="$2"
	local sha256="$3"
	if [[ ! "$sha256" =~ ^[0-9a-f]{64}$ ]]; then
		printf 'ERROR: a pinned SHA-256 is required before downloading %s\n' "$url" >&2
		return 1
	fi

	if ! curl --retry 5 --retry-delay 3 --retry-connrefused --proto '=https' --tlsv1.2 -sSfL "$url" -o "$destination"; then
		rm -f "$destination"
		return 1
	fi
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

ci_print_already_installed() {
	local label="$1"
	local version="$2"
	printf '%s already installed: %s\n\n' "$label" "$version"
}

ci_print_installed() {
	local label="$1"
	local version="$2"
	printf '%s installed successfully: %s\n\n' "$label" "$version"
}
