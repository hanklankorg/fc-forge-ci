#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
set -euo pipefail

# GoReleaser invokes this only after archives, SBOMs and checksums exist, but
# before signing or upload. Compare the actual payload about to be signed.
[[ $# == 7 && $1 == release && $2 == sign && $3 == --method=sigstore &&
	$4 == --no-checksums-file && $5 == --no-release-artifacts-dir && $6 == --file ]] || {
	printf 'Expected the GoReleaser checksum-only signing arguments.\n' >&2
	exit 2
}
manifest=$7
[[ ${manifest##*/} == checksums.txt && -f $manifest && ! -L $manifest ]] || exit 2
directory=$(dirname -- "$manifest")
if [[ -n ${FORGE_CI_RELEASE_REFERENCE:-} ]]; then
	reference=$(realpath -e -- "$FORGE_CI_RELEASE_REFERENCE")
	[[ $reference != "$(realpath -e -- "$directory")" ]] || {
		printf 'Release reference must be a separate rebuild directory.\n' >&2
		exit 2
	}
	bash "$(dirname -- "${BASH_SOURCE[0]}")/verify-reproducible.sh" --compare "$reference" "$directory"
elif [[ ${GORELEASER_CURRENT_TAG:-} == *-pre ]]; then
	printf 'Pre-release %s: reproducible final-tag comparison skipped\n' "$GORELEASER_CURRENT_TAG" >&2
else
	: "${FORGE_CI_RELEASE_REFERENCE:?Prepare an unsigned final-tag rebuild before signing}"
fi
shopt -s nullglob
# SBOM bytes are not reproducible, but any published SBOM still needs coverage
# by the signed manifest just as its archive does.
for archive in "$directory"/*.tar.gz "$directory"/*.tar.gz.cdx.sbom.json; do
	awk -v expected="${archive##*/}" '
    $2 == expected || $2 == "*" expected {
      count++
      if (NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-fA-F]/) invalid = 1
    }
    END { exit(count != 1 || invalid) }
  ' "$manifest" || {
		printf 'Expected exactly one valid checksum for %s\n' "${archive##*/}" >&2
		exit 1
	}
done
(
	cd "$directory"
	sha256sum --strict --check checksums.txt
)
exec forge-ci "$@"
