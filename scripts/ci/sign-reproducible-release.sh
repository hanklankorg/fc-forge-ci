#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
# Compares the release payload with the final-tag rebuild, validates the
# checksums, then runs the given `forge-ci release sign` command. Run by
# GoReleaser's signs step (.goreleaser.yml) and by the GitHub release workflows
# (release-binary.yml, build-cli.yml) after GoReleaser.
set -euo pipefail

# GitHub's release workflows invoke this after GoReleaser has produced unsigned
# archives, SBOMs and checksums, but before signing or upload. Compare the actual
# payload about to be signed.
[[ $# == 7 && $1 == release && $2 == sign && $3 == --method=sigstore &&
  $4 == --no-checksums-file && $5 == --no-release-artifacts-dir && $6 == --file ]] || {
  printf 'Expected checksum-only signing arguments.\n' >&2
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
# Reuse the release owner's exact subject coverage and digest validation. The
# unsigned GoReleaser producer supplies artifacts.json; this step executes none.
# Metadata subjects are workspace-relative, even if the signer's file argument
# spells this workspace's dist as an absolute path.
forge-ci release files validate-checksums --dist-dir "${directory#"$PWD"/}" --checksums-file "$manifest"
exec forge-ci "$@"
