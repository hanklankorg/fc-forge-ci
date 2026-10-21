#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
set -euo pipefail

# Keep this inventory in step with .goreleaser.yml. Checking the inventory as
# well as equality prevents two identically incomplete builds from passing.
expected=(
	'forge-ci_*_linux_amd64.tar.gz'
	'forge-ci_*_linux_arm64.tar.gz'
	'forge-ci_*_darwin_amd64.tar.gz'
	'forge-ci_*_darwin_arm64.tar.gz'
)

compare() {
	local first=$1 second=$2 directory pattern file count
	local -a files
	shopt -s nullglob
	for directory in "$first" "$second"; do
		files=("$directory"/*.tar.gz "$directory"/*.deb "$directory"/*.rpm "$directory"/*.apk)
		if ((${#files[@]} != ${#expected[@]})); then
			printf 'Expected %s release files in %s; found %s\n' "${#expected[@]}" "$directory" "${#files[@]}" >&2
			return 1
		fi
		for pattern in "${expected[@]}"; do
			count=0
			for file in "${files[@]}"; do
				# shellcheck disable=SC2053 # Match the release inventory's glob.
				if [[ ${file##*/} == $pattern && -f $file && -s $file && ! -L $file ]]; then
					count=$((count + 1))
				fi
			done
			if ((count != 1)); then
				printf 'Expected exactly one %s in %s; found %s\n' "$pattern" "$directory" "$count" >&2
				return 1
			fi
		done
	done
	files=("$first"/*.tar.gz "$first"/*.deb "$first"/*.rpm "$first"/*.apk)
	for file in "${files[@]}"; do
		if ! cmp -s -- "$file" "$second/${file##*/}"; then
			printf 'Different or missing: %s and %s/%s\n' "$file" "$second" "${file##*/}" >&2
			return 1
		fi
	done
	printf '%s release files are byte-identical\n' "${#files[@]}"
}

release_tag='' reference=''
if [[ $# == 3 && $1 == --compare ]]; then
	compare "$2" "$3"
	exit
elif [[ $# == 3 && $1 == --release-reference ]]; then
	release_tag=$2 reference=$3
	[[ $release_tag == v[0-9]* && $reference == /* && ! -e $reference && ! -L $reference ]] || {
		printf 'Provide a final v-prefixed tag and a new absolute reference directory.\n' >&2
		exit 2
	}
elif (($# != 0)); then
	printf 'Usage: %s [--compare FIRST_DIST SECOND_DIST | --release-reference TAG OUTPUT]\n' "$0" >&2
	exit 2
fi

root=$(git rev-parse --show-toplevel)
if [[ -n $(git -C "$root" status --porcelain --untracked-files=normal) ]]; then
	printf 'Use a clean committed checkout; local edits are not included in the rebuild.\n' >&2
	exit 2
fi
commit=$(git -C "$root" rev-parse HEAD)
if [[ -n $release_tag && $(git -C "$root" rev-parse "refs/tags/$release_tag^{commit}") != "$commit" ]]; then
	printf 'The final release tag must name the checked-out commit.\n' >&2
	exit 2
fi
export GOTOOLCHAIN=local GOWORK=off GOENV=off GOFLAGS=-mod=readonly
export GOAMD64=v1 GOARM64=v8.0 TZ=UTC LC_ALL=C
export GOPROXY=off GOSUMDB=off
unset GORELEASER_CURRENT_TAG GORELEASER_PREVIOUS_TAG
export SOURCE_DATE_EPOCH
SOURCE_DATE_EPOCH=$(git -C "$root" log -1 --format=%ct "$commit")
go version
goreleaser --version
printf 'Source: %s; version: %s; module downloads: disabled\n' "$commit" "${release_tag:-unsigned snapshot}"
work=$(mktemp -d "${TMPDIR:-/tmp}/forge-ci-repro.XXXXXXXX")
cleanup() {
	local status=$?
	if ((status == 0)); then
		rm -rf -- "$work"
	else
		printf 'Build/comparison failed. Outputs retained in %s (inspect with diffoscope).\n' "$work" >&2
	fi
}
trap cleanup EXIT
if [[ -n $release_tag ]]; then
	git clone --quiet --no-hardlinks --no-checkout "$root" "$work/reference"
	git -C "$work/reference" checkout --quiet --detach "$commit"
	(
		cd "$work/reference"
		GOCACHE="$work/reference-cache" GORELEASER_CURRENT_TAG="$release_tag" \
			goreleaser release --clean --skip=publish,sign,sbom,announce --parallelism=2
	)
	compare "$work/reference/dist" "$work/reference/dist"
	cp -R "$work/reference/dist" "$reference"
	printf 'Unsigned final-tag reference retained in %s\n' "$reference"
	exit
fi
for pass in first second; do
	git clone --quiet --no-hardlinks --no-checkout "$root" "$work/$pass"
	git -C "$work/$pass" checkout --quiet --detach "$commit"
	(
		cd "$work/$pass"
		export GOCACHE="$work/$pass-cache"
		goreleaser release --snapshot --clean --skip=publish,sign,sbom,announce --parallelism=2
	)
done
compare "$work/first/dist" "$work/second/dist"
