#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Install forge-ci on plain/macOS runners where the runtime container is not
# available.
#
# Resolution:
#   "local" / "."        → go install ./cmd/forge-ci (working copy)
#   vX.Y.Z[(-suffix)]    → download release tarball + verify SHA-256
#   anything else (SHAs, branches) → fetch the requested Git revision and build
#                                     ./cmd/forge-ci from that detached checkout
#
# Release refs use only the authenticated release-asset path and fail closed on
# any download or verification error. Set FORGE_CI_USE_GO_INSTALL=1 only
# when explicitly choosing source installation instead.
#
# FORGE_CI_REPOSITORY (owner/name) and FORGE_CI_PRERELEASE_BRANCH select
# the repository and branch whose releases and signatures are trusted, so a fork
# installs and verifies its own binaries.

# shellcheck source=install-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-common.sh"
# shellcheck source=install-cosign.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-cosign.sh"

# Repository owner/name, branch and release URL base are env-overridable so a
# fork (or a test) can point at its own. Defaults match the upstream repository.
: "${FORGE_CI_REPOSITORY:=diggsweden/forge-ci}"
: "${FORGE_CI_PRERELEASE_BRANCH:=main}"
: "${FORGE_CI_SERVER_URL:=${GITHUB_SERVER_URL:-https://github.com}}"
: "${FORGE_CI_RELEASE_URL_BASE:=${FORGE_CI_SERVER_URL}/${FORGE_CI_REPOSITORY}/releases/download}"

# check_forge_ci_source refuses repository and branch values that are not a
# plain owner/name and branch name, before they reach a regex or a git URL.
check_forge_ci_source() {
	if [[ ! "$FORGE_CI_REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ||
		"/${FORGE_CI_REPOSITORY}/" == *'/./'* || "/${FORGE_CI_REPOSITORY}/" == *'/../'* ]]; then
		printf 'ERROR: FORGE_CI_REPOSITORY must be owner/name, got %q\n' "$FORGE_CI_REPOSITORY" >&2
		return 1
	fi
	if [[ ! "$FORGE_CI_PRERELEASE_BRANCH" =~ ^[A-Za-z0-9._/]+(-[A-Za-z0-9._/]*)*$ ]]; then
		printf 'ERROR: FORGE_CI_PRERELEASE_BRANCH must be a plain branch name, got %q\n' "$FORGE_CI_PRERELEASE_BRANCH" >&2
		return 1
	fi
}

# resolve_forge_ci_os prints linux|darwin, or fails on unsupported hosts.
resolve_forge_ci_os() {
	local os
	os="$(uname -s)"
	case "$os" in
	Linux) printf 'linux' ;;
	Darwin) printf 'darwin' ;;
	*)
		printf 'ERROR: Unsupported forge-ci OS: %s\n' "$os" >&2
		return 1
		;;
	esac
}

# resolve_forge_ci_arch prints amd64|arm64, or fails on unsupported hosts.
resolve_forge_ci_arch() {
	local arch
	arch="$(uname -m)"
	case "$arch" in
	x86_64 | amd64) printf 'amd64' ;;
	aarch64 | arm64) printf 'arm64' ;;
	*)
		printf 'ERROR: Unsupported forge-ci arch: %s\n' "$arch" >&2
		return 1
		;;
	esac
}

# resolve_forge_ci_dist prints the release-asset filename for (ref, os, arch).
# Strips the leading "v" from the version because GoReleaser does the same.
resolve_forge_ci_dist() {
	local ref="$1" os="$2" arch="$3"
	local version="${ref#v}"
	printf 'forge-ci_%s_%s_%s.tar.gz' "$version" "$os" "$arch"
}

# verify_forge_ci_binary_pin enforces an exact SHA-256 pin on the
# INSTALLED BINARY when FORGE_CI_BINARY_SHA256 is set (the same pin
# variable forgejo-ci's signer toolchain asserts). This is defense in
# depth on top of cosign + checksums.txt and also covers explicit source
# installs. Fail-closed: on
# mismatch the binary is removed and the install errors.
verify_forge_ci_binary_pin() {
	local binary="$1"
	local pin="${FORGE_CI_BINARY_SHA256:-}"
	[[ -z "$pin" ]] && return 0
	local got
	if command -v sha256sum &>/dev/null; then
		got="$(sha256sum "$binary" | awk '{print $1}')"
	elif command -v shasum &>/dev/null; then
		got="$(shasum -a 256 "$binary" | awk '{print $1}')"
	else
		printf 'ERROR: FORGE_CI_BINARY_SHA256 set but no sha256 helper available\n' >&2
		rm -f "$binary"
		return 1
	fi
	if [[ "$got" != "$pin" ]]; then
		printf 'ERROR: installed forge-ci does not match FORGE_CI_BINARY_SHA256:\n  got:  %s\n  want: %s\n' "$got" "$pin" >&2
		rm -f "$binary"
		return 1
	fi
	printf 'Binary pin: forge-ci sha256 %s verified\n' "$pin"
}

# is_forge_ci_release_ref reports whether ref looks like a published semver
# tag (vN.N.N with optional -suffix). Branch names and SHAs fall through.
is_forge_ci_release_ref() {
	local ref="$1"
	[[ "$ref" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9._-]+)?$ ]]
}

is_forge_ci_commit_ref() {
	[[ "$1" =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]]
}

# GitHub's latest-release redirect excludes drafts and prereleases. Resolve it
# once, then use the concrete tag for archive URLs and signature identity.
resolve_forge_ci_stable_release() {
	local release_base="${FORGE_CI_RELEASE_URL_BASE%/download}" resolved ref
	resolved="$(curl --retry 5 --retry-delay 3 --retry-connrefused -sSfL -o /dev/null -w '%{url_effective}' "$release_base/latest")" || return 1
	ref="${resolved##*/}"
	if [[ ! "$ref" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ || "$resolved" != "$release_base/tag/$ref" ]]; then
		printf 'ERROR: latest forge-ci release did not resolve to a stable version tag\n' >&2
		return 1
	fi
	printf '%s' "$ref"
}

# verify_forge_ci_sha256 reads a goreleaser-style checksums.txt
# ("<hex>  <filename>") and verifies <archive> matches.
verify_forge_ci_sha256() {
	local archive="$1" checksums="$2"
	local want got name
	name="$(basename "$archive")"
	want="$(awk -v n="$name" '$2 == n {print $1; exit}' "$checksums")"
	if [[ -z "$want" ]]; then
		printf 'ERROR: %s not in checksums.txt\n' "$name" >&2
		return 1
	fi
	if command -v sha256sum &>/dev/null; then
		got="$(sha256sum "$archive" | awk '{print $1}')"
	elif command -v shasum &>/dev/null; then
		got="$(shasum -a 256 "$archive" | awk '{print $1}')"
	else
		printf 'ERROR: neither sha256sum nor shasum available for SHA-256 verification\n' >&2
		return 1
	fi
	if [[ "$got" != "$want" ]]; then
		printf 'ERROR: %s SHA-256 mismatch:\n  got:  %s\n  want: %s\n' "$name" "$got" "$want" >&2
		return 1
	fi
}

# verify_forge_ci_cosign verifies the Sigstore v3 bundle alongside
# the checksums file. FAIL-CLOSED: a missing cosign binary or a missing
# signature bundle is an error.
#
# Identity is pinned to the FORGE_CI_REPOSITORY workflow that produced the
# signature. Two trust domains, selected by ref (see _forge_ci_cosign_identity):
#   vN.N.N      -> release-binary.yml called from refs/tags/release-request/vN.N.N (production)
#   *-pre      -> build-cli.yml on FORGE_CI_PRERELEASE_BRANCH (rolling pre-release channel)
# A signature from any other repository, workflow or ref fails the check even if
# cosign accepts the bundle. FORGE_CI_COSIGN_IDENTITY overrides both.
_forge_ci_cosign_identity() {
	local ref="$1"
	if [[ -n "${FORGE_CI_COSIGN_IDENTITY:-}" ]]; then
		printf '%s' "$FORGE_CI_COSIGN_IDENTITY"
		return 0
	fi
	check_forge_ci_source || return 1
	# Validated values hold no regex metacharacter other than ".".
	local workflows="${FORGE_CI_SERVER_URL//./\\.}/${FORGE_CI_REPOSITORY//./\\.}/\.github/workflows"
	if [[ "$ref" == *-pre ]]; then
		printf '%s' "^${workflows}/build-cli\.yml@refs/heads/${FORGE_CI_PRERELEASE_BRANCH//./\\.}\$"
	else
		# Bound to the exact version: a signature made for another release
		# request does not verify this one.
		printf '%s' "^${workflows}/release-binary\.yml@refs/tags/release-request/${ref//./\\.}\$"
	fi
}

verify_forge_ci_cosign() {
	local checksums="$1" bundle="$2" ref="${3:-}"

	if ! command -v cosign &>/dev/null; then
		printf 'ERROR: cosign not on PATH; install cosign with scripts/bootstrap/install-cosign.sh\n' >&2
		return 1
	fi

	if [[ ! -f "$bundle" ]]; then
		printf 'ERROR: %s missing; the release carries no signature bundle\n' "$bundle" >&2
		return 1
	fi

	local identity issuer
	identity="$(_forge_ci_cosign_identity "$ref")" || return 1
	issuer="${FORGE_CI_COSIGN_ISSUER:-https://token.actions.githubusercontent.com}"

	if ! cosign verify-blob \
		--bundle "$bundle" \
		--new-bundle-format \
		--certificate-identity-regexp "$identity" \
		--certificate-oidc-issuer "$issuer" \
		"$checksums" >/dev/null 2>&1; then
		printf 'ERROR: cosign verification of %s failed against identity %s\n' "$(basename "$checksums")" "$identity" >&2
		return 1
	fi

	printf 'Sigstore: %s verified (identity-regex %s)\n' "$(basename "$checksums")" "$identity"
}

bootstrap_forge_ci_cosign() {
	# This cosign verifies the download and goes with the temporary directory
	# it lives in, so it must not reach later steps through $GITHUB_PATH.
	if ! GITHUB_PATH='' FORGEJO_PATH='' install_cosign; then
		printf 'ERROR: failed to bootstrap the repository-pinned cosign used to verify forge-ci\n' >&2
		return 1
	fi
}

# install_forge_ci_release verifies a privately staged binary before replacing
# the installed file. The subshell owns bootstrap PATH changes and cleanup traps.
install_forge_ci_release() (
	local ref="$1" install_dir="$2"
	if ! command -v curl &>/dev/null; then
		return 1
	fi
	local os arch dist tmp candidate=""
	os="$(resolve_forge_ci_os)" || return 1
	arch="$(resolve_forge_ci_arch)" || return 1
	dist="$(resolve_forge_ci_dist "$ref" "$os" "$arch")"
	tmp="$(mktemp -d)" || return 1
	trap 'rm -rf "$tmp"; [[ -z "$candidate" ]] || rm -f "$candidate"' EXIT

	# Retry transient network / 5xx failures (e.g. GitHub Releases returning a
	# 504 gateway timeout). `--retry` already covers HTTP 408/429/5xx + timeouts;
	# `--retry-connrefused` adds connection-refused. Both are old/portable flags.
	local -a curl_retry=(--retry 5 --retry-delay 3 --retry-connrefused)

	local asset_url="${FORGE_CI_RELEASE_URL_BASE}/${ref}/${dist}"
	local sums_url="${FORGE_CI_RELEASE_URL_BASE}/${ref}/checksums.txt"
	local bundle_url="${FORGE_CI_RELEASE_URL_BASE}/${ref}/checksums.txt.bundle"
	CI_TEMP_DIR="$tmp/bootstrap" bootstrap_forge_ci_cosign || return 1
	printf 'Downloading forge-ci %s (%s/%s)...\n' "$ref" "$os" "$arch"
	if ! curl "${curl_retry[@]}" -sSfL -o "$tmp/$dist" "$asset_url"; then
		printf 'WARN: failed to download %s\n' "$asset_url" >&2
		return 1
	fi
	if ! curl "${curl_retry[@]}" -sSfL -o "$tmp/checksums.txt" "$sums_url"; then
		printf 'WARN: failed to download %s\n' "$sums_url" >&2
		return 1
	fi
	if ! curl "${curl_retry[@]}" -sSfL -o "$tmp/checksums.txt.bundle" "$bundle_url"; then
		printf 'ERROR: failed to download %s\n' "$bundle_url" >&2
		return 1
	fi
	if ! verify_forge_ci_cosign "$tmp/checksums.txt" "$tmp/checksums.txt.bundle" "$ref"; then
		return 1
	fi
	if ! verify_forge_ci_sha256 "$tmp/$dist" "$tmp/checksums.txt"; then
		return 1
	fi
	mkdir "$tmp/extracted" || return 1
	if ! tar -xzf "$tmp/$dist" -C "$tmp/extracted" forge-ci; then
		printf 'ERROR: failed to extract forge-ci from %s\n' "$dist" >&2
		return 1
	fi
	if [[ -L "$tmp/extracted/forge-ci" || ! -f "$tmp/extracted/forge-ci" ]]; then
		printf 'ERROR: release binary must be a non-symlink regular file\n' >&2
		return 1
	fi
	verify_forge_ci_binary_pin "$tmp/extracted/forge-ci" || return 1
	if [[ -L "$install_dir" || -L "$install_dir/forge-ci" || (-e "$install_dir/forge-ci" && ! -f "$install_dir/forge-ci") ]]; then
		printf 'ERROR: unsafe forge-ci install destination\n' >&2
		return 1
	fi
	mkdir -p "$install_dir" || return 1
	candidate="$(mktemp "$install_dir/.forge-ci.XXXXXXXX")" || return 1
	cp "$tmp/extracted/forge-ci" "$candidate" || return 1
	chmod 0755 "$candidate" || return 1
	mv -f "$candidate" "$install_dir/forge-ci" || return 1
)

install_forge_ci_go_install() {
	local ref="$1" install_dir="$2"
	if ! command -v go &>/dev/null; then
		printf 'ERROR: go binary not found for requested source installation\n' >&2
		return 1
	fi
	mkdir -p "$install_dir"
	if [[ "$ref" == "local" || "$ref" == "." ]]; then
		GOBIN="$install_dir" go install ./cmd/forge-ci || return 1
	else
		install_forge_ci_source "$ref" "$install_dir" || return 1
	fi
	verify_forge_ci_binary_pin "$install_dir/forge-ci" || return 1
}

# Build the requested Git revision directly, including for upstream. A Go module
# query can map a SHA to the moving v0.90.0-pre tag and reuse immutable cached
# module contents from an older commit. Forks also need Git rather than module
# resolution, since their module path can still identify the upstream repository.
install_forge_ci_source() (
	local ref="$1" install_dir="$2" src source_sha remote advertised advertised_sha rest object_format=sha1
	if ! command -v git &>/dev/null; then
		printf 'ERROR: git binary not found for requested source installation\n' >&2
		return 1
	fi
	check_forge_ci_source || return 1
	if [[ ! "$ref" =~ ^[A-Za-z0-9._/]+(-[A-Za-z0-9._/]*)*$ ]]; then
		printf 'ERROR: forge-ci ref must be a plain branch, tag or commit, got %q\n' "$ref" >&2
		return 1
	fi
	src="$(mktemp -d)" || return 1
	trap 'rm -rf "$src"' EXIT
	remote="${FORGE_CI_SERVER_URL}/${FORGE_CI_REPOSITORY}.git"
	advertised_sha="$ref"
	if ! is_forge_ci_commit_ref "$ref"; then
		advertised="$(git ls-remote -- "$remote" "$ref" "refs/heads/$ref" "refs/tags/$ref")" || return 1
		read -r advertised_sha rest <<<"$advertised"
	fi
	if ! is_forge_ci_commit_ref "$advertised_sha"; then
		printf 'ERROR: could not resolve forge-ci ref %s; use a full 40- or 64-character commit SHA or an existing branch/tag\n' "$ref" >&2
		return 1
	fi
	[[ "${#advertised_sha}" != 64 ]] || object_format=sha256
	git -C "$src" init -q --object-format="$object_format" || return 1
	if ! git -C "$src" fetch -q --depth 1 "$remote" "$ref"; then
		printf 'ERROR: could not fetch forge-ci ref %s; use a full 40- or 64-character commit SHA or an existing branch/tag\n' "$ref" >&2
		return 1
	fi
	git -C "$src" checkout -q --detach FETCH_HEAD || return 1
	source_sha="$(git -C "$src" rev-parse HEAD)" || return 1
	if is_forge_ci_commit_ref "$ref" && [[ "$source_sha" != "$ref" ]]; then
		printf 'ERROR: forge-ci source commit %s does not match requested %s\n' "$source_sha" "$ref" >&2
		return 1
	fi
	printf 'Building forge-ci from %s at commit %s\n' "$FORGE_CI_REPOSITORY" "$source_sha"
	(cd "$src" && GOBIN="$install_dir" go install ./cmd/forge-ci) || return 1
)

install_forge_ci() {
	local ref="${1:-${FORGE_CI_BINARY_REF:-}}"
	local install_dir="${FORGE_CI_INSTALL_DIR:-${RUNNER_TEMP:-${CI_TEMP_DIR:-/tmp}}/bin}"
	# Go requires an absolute GOBIN, including after entering a source checkout.
	[[ "$install_dir" == /* ]] || install_dir="$PWD/$install_dir"

	if [[ -z "$ref" ]]; then
		ref="$(resolve_forge_ci_stable_release)" || return 1
	fi
	printf 'Installing forge-ci (%s) into %s...\n' "$ref" "$install_dir"
	if [[ "$ref" == "local" || "$ref" == "." ]]; then
		if [[ "${FORGE_CI_SOURCE_SHA+x}" == x ]]; then
			printf 'ERROR: local source installation cannot use FORGE_CI_SOURCE_SHA; request the bound commit instead\n' >&2
			return 1
		fi
		install_forge_ci_go_install "$ref" "$install_dir" || return 1
	elif [[ "${FORGE_CI_USE_GO_INSTALL:-0}" != "1" ]] && is_forge_ci_release_ref "$ref"; then
		install_forge_ci_release "$ref" "$install_dir" || return 1
	else
		# Workflows bind source builds to the commit of their bootstrap checkout.
		# Release-asset installs above retain the original tag and signature policy.
		if [[ "${FORGE_CI_SOURCE_SHA+x}" == x ]]; then
			if ! is_forge_ci_commit_ref "$FORGE_CI_SOURCE_SHA"; then
				printf 'ERROR: FORGE_CI_SOURCE_SHA must be a full 40- or 64-character commit SHA\n' >&2
				return 1
			fi
			if is_forge_ci_commit_ref "$ref" && [[ "$ref" != "$FORGE_CI_SOURCE_SHA" ]]; then
				printf 'ERROR: bootstrap source commit does not match requested forge-ci commit\n' >&2
				return 1
			fi
			ref="$FORGE_CI_SOURCE_SHA"
		fi
		install_forge_ci_go_install "$ref" "$install_dir" || return 1
	fi

	ci_prepend_path "$install_dir"
	ci_require_command forge-ci forge-ci || return 1
	ci_print_installed "forge-ci" "$(forge-ci --version 2>/dev/null || printf 'version unavailable')"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	install_forge_ci "$@"
fi
