#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# The delivery layers' engine binary: the one place that says how it is built,
# recorded and published.
#
#   forgejo-binary.sh build <arch> <output>    build this tree's CLI to <output>
#   forgejo-binary.sh refresh                  record each build's sha256 in the tree
#   forgejo-binary.sh publish <arch> <binary>  upload a build the tree records
#   forgejo-binary.sh inputs                   print the build inputs, one path a line
#
# <arch> is amd64 or arm64. The tree tracks one checksum per architecture,
# .forgejo/bin/forge-ci-linux-<arch>.sha256, and not the binaries. A job
# downloads the binary of its host's architecture by that checksum from the
# generic package registry of the forge that serves the layer, and verifies
# it; see "The binary in the layer" in docs/forgejo.md.
#
# The build depends on the Go sources and the Go release alone: linux/<arch>,
# cross-compiled on any host, no cgo, trimmed paths, no VCS stamp, one
# constant version string, modules from the local cache only. It names no
# commit and no date, so two commits with the same build inputs (cmd/,
# internal/, go.mod, go.sum) build the same bytes, and test/forgejobehavior
# holds the recorded checksums to this build.
set -euo pipefail
umask 022

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd -P)
go_bin=${GO:-go}
build_inputs=(cmd internal go.mod go.sum)
architectures=(amd64 arm64)
# What `forge-ci --version` of the layer's binary answers. The sha256 is
# its identity; setup-forge-ci prints both.
version=forgejo-layer

usage() {
	printf 'usage: %s build <amd64|arm64> <output>\n       %s refresh\n       %s publish <amd64|arm64> <binary>\n       %s inputs\n' "$0" "$0" "$0" "$0" >&2
	exit 2
}

fail() {
	printf 'forgejo-binary: %s\n' "$1" >&2
	exit 1
}

# require_arch accepts the architectures the layer is built for.
require_arch() {
	[[ " ${architectures[*]} " == *" $1 "* ]] || usage
}

# The tracked checksum of the architecture's build, relative to the tree.
checksum_path() {
	printf '.forgejo/bin/forge-ci-linux-%s.sha256\n' "$1"
}

# go_env ARCH COMMAND... runs a command with the build's whole environment
# for linux/ARCH. Cache and module
# locations are passed through when set: they say where files are, not what
# they are.
go_env() {
	local arch=$1
	shift
	local -a environment=(
		"HOME=${HOME:?}"
		"PATH=${PATH:?}"
		LANG=C.UTF-8
		LC_ALL=C.UTF-8
		GOTOOLCHAIN=local
		GOPROXY=off
		GOSUMDB=off
		GOWORK=off
		GOENV=off
		GOFLAGS=
		CGO_ENABLED=0
		GOOS=linux
		"GOARCH=${arch}"
	)
	local name
	for name in GOPATH GOMODCACHE GOCACHE; do
		[[ -z "${!name:-}" ]] || environment+=("${name}=${!name}")
	done
	env -i "${environment[@]}" "$@"
}

# The toolchain is part of the build: another Go release writes other bytes.
require_pinned_toolchain() {
	local pinned actual
	pinned=go$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' "$repo_root/go.mod")
	actual=$(go_env amd64 "$go_bin" env GOVERSION)
	[[ "$actual" == "$pinned" ]] ||
		fail "the toolchain is $actual, and go.mod pins $pinned: the binary is only reproducible with the pinned one (mise install)"
}

# build ARCH OUTPUT
build() {
	go_env "$1" "$go_bin" -C "$repo_root" build \
		-trimpath \
		-buildvcs=false \
		-ldflags="-s -w -X main.version=${version}" \
		-o "$2" ./cmd/forge-ci
}

sha256_of() {
	local sum
	sum=$(sha256sum -- "$1")
	printf '%s\n' "${sum%% *}"
}

# The digest the tree records for ARCH: one `<sha256>  bin/forge-ci` line, the
# name relative to .forgejo/, the layer root the resolver anchors on.
recorded_sha256() {
	local file digest name extra
	file=$repo_root/$(checksum_path "$1")
	read -r digest name extra <"$file" || true
	[[ "$digest" =~ ^[0-9a-f]{64}$ && "$name" == bin/forge-ci && -z "$extra" ]] ||
		fail "$file is not one '<sha256>  bin/forge-ci' line; rewrite it with \`just refresh-forgejo-binary\`"
	printf '%s\n' "$digest"
}

# publish uploads a build to the generic packages of the engine repository's
# owner, under the sha256 as its version, and then downloads it the way a job
# does. The storage is not trusted: a job accepts only the bytes the pinned
# commit's checksum names, so an upload of anything else is refused here
# before it is made.
publish() {
	local arch=$1 binary=$2 digest recorded url status authorization
	[[ -f "$binary" && ! -L "$binary" ]] || fail "not a regular file: $binary"
	command -v curl >/dev/null 2>&1 || fail 'curl is needed to publish'
	digest=$(sha256_of "$binary")
	recorded=$(recorded_sha256 "$arch")
	[[ "$digest" == "$recorded" ]] ||
		fail "the build is not the linux/$arch one the tree records, so it is not published.
  built:    $digest
  recorded: $recorded
The checksum was recorded from another toolchain or other modules than this job used, or a build input changed after it was recorded."
	[[ -n "${FORGE_CI_PACKAGE_TOKEN:-}" ]] ||
		fail 'FORGE_CI_PACKAGE_TOKEN is empty: a token that may write to the engine package registry'

	# shellcheck source=.forgejo/bin/lib-forge-ci.sh
	source "$repo_root/.forgejo/bin/lib-forge-ci.sh"
	url=$(forge_ci_package_url "$recorded" "$arch") || exit 1
	authorization='Authorization: token'
	if [[ "${FORGE_CI_PACKAGE_PROVIDER:-forgejo}" == gitlab ]]; then
		case "${FORGE_CI_PACKAGE_TOKEN_TYPE:-private}" in
		private) authorization='PRIVATE-TOKEN:' ;;
		job) authorization='JOB-TOKEN:' ;;
		*) fail 'FORGE_CI_PACKAGE_TOKEN_TYPE must be private or job for GitLab' ;;
		esac
	fi

	# Not local: the exit trap reads it after this function has returned.
	staging=$(mktemp -d "${TMPDIR:-/tmp}/forge-ci-forgejo-binary.XXXXXXXX")
	trap 'rm -rf -- "$staging"' EXIT

	# The token is read from standard input, so it is in no argument list.
	status=$(printf 'header = "%s %s"\n' "$authorization" "$FORGE_CI_PACKAGE_TOKEN" |
		curl --config - --proto '=https' --silent --show-error --max-time 600 \
			--output /dev/null --write-out '%{http_code}' \
			--upload-file "$binary" "$url") || fail "upload failed: $url"
	case "$status" in
	201) printf 'Uploaded %s\n' "$url" ;;
	409) printf 'Already published, and a version cannot be replaced: %s\n' "$url" ;;
	*) fail "upload answered HTTP $status: $url
The token must be able to write to the destination package registry." ;;
	esac

	# What is there now is what jobs get, whoever uploaded it.
	download_forge_ci "$url" "$recorded" "$staging/forge-ci" || exit 1
	printf 'Published and verified, readable without credentials:\n  %s\n  sha256 %s\n' "$url" "$recorded"
}

case "${1:-}" in
build)
	[[ $# -eq 3 && -n "$3" ]] || usage
	require_arch "$2"
	# Another Go release writes other bytes without saying so, and every
	# caller compares the result with the recorded checksum.
	require_pinned_toolchain
	build "$2" "$3"
	;;
refresh)
	[[ $# -eq 1 ]] || usage
	require_pinned_toolchain
	staging=$(mktemp -d "${TMPDIR:-/tmp}/forge-ci-forgejo-binary.XXXXXXXX")
	trap 'rm -rf -- "$staging"' EXIT
	# Every build first, so a failed one records nothing.
	for arch in "${architectures[@]}"; do
		build "$arch" "$staging/forge-ci-$arch"
		printf '%s  bin/forge-ci\n' "$(sha256_of "$staging/forge-ci-$arch")" >"$staging/forge-ci-$arch.sha256"
	done
	for arch in "${architectures[@]}"; do
		install -m 0644 -- "$staging/forge-ci-$arch.sha256" "$repo_root/$(checksum_path "$arch")"
		printf 'Recorded the linux/%s build of this tree in %s\n  sha256 %s\n' \
			"$arch" "$(checksum_path "$arch")" "$(recorded_sha256 "$arch")"
	done
	if [[ -z "$(git -C "$repo_root" status --porcelain=v1 -- .forgejo/bin/)" ]]; then
		printf 'The recorded checksums already matched; nothing to commit.\n'
	else
		printf 'Commit the files, with the change they follow or after it.\n'
	fi
	;;
publish)
	[[ $# -eq 3 && -n "$3" ]] || usage
	require_arch "$2"
	publish "$2" "$3"
	;;
inputs)
	[[ $# -eq 1 ]] || usage
	printf '%s\n' "${build_inputs[@]}"
	;;
*) usage ;;
esac
