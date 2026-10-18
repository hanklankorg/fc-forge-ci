#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release build, called by the pinned release policy in a key-free job. The
# Containerfile is the build: its builder stage compiles the service. It
# leaves the unsigned hand-off in dist/: the binary extracted from the
# Containerfile's export-binary stage, the ledger of the runtime image it
# pushed for staging, release notes, checksums and the manifest. Forge CI's
# signer jobs sign and publish it.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

name=hello-cargo-container
cargo test --locked

mkdir -p dist
if ! forge-ci release notes --changelog-file CHANGELOG.md \
	--release-version "${RELEASE_TAG}" --target-file dist/release-notes.md; then
	forge-ci release notes --source-file /dev/null/absent --target-file dist/release-notes.md \
		--release-version "${RELEASE_TAG}" --release-commit "${RELEASE_SHA}"
fi
# The runtime image goes to this project's registry as an untrusted staging
# image; the signing jobs import it into the publication project. The job
# token pushes it, from an auth file that lives only in this job. The
# builder stage RUNs cargo, so the image builds for this job's own
# platform, linux/amd64, only.
auth_dir=$(mktemp -d)
trap 'rm -rf -- "${auth_dir}"' EXIT
printf '%s' "${CI_JOB_TOKEN:?}" | forge-ci container login --registry "${CI_REGISTRY:?}" \
	--registry-username gitlab-ci-token --registry-password-file - --auth-file "${auth_dir}/auth.json"
CI_OUTPUT="${auth_dir}/image.env" forge-ci container build-push-oci-image \
	--tag "${RELEASE_TAG}" --containerfile Containerfile --context . \
	--builds-json '[{"platform":"linux/amd64"}]' --image "${CI_REGISTRY_IMAGE:?}" \
	--title "${name}" --auth-file "${auth_dir}/auth.json" \
	--scan-output-template 'dist/trivy-results-runtime-{arch}.json'
# The outputs are GitLab dotenv lines, their keys in upper case.
image='' digest=''
while IFS= read -r line || [ -n "${line}" ]; do
	case "${line}" in
	IMAGE=*) image="${line#IMAGE=}" ;;
	DIGEST=*) digest="${line#DIGEST=}" ;;
	esac
done <"${auth_dir}/image.env"
forge-ci container ledger add --role runtime --image-name "${image:?}" --digest "${digest:?}" \
	--final-tag-name "${RELEASE_TAG}" --tag "${RELEASE_TAG}" --default-sbom

# extract.binary: the export-binary stage of the same Containerfile,
# exported as files, its binary renamed <name>-linux-amd64.
forge-ci container build --mode local --target export-binary \
	--file Containerfile --context . --platform linux/amd64 \
	--auth-file "${auth_dir}/auth.json" --output-dir dist/linux-amd64
forge-ci container suffix-extracted-binaries \
	--binaries-dir dist/linux-amd64 --arch amd64 --expected-names "${name}"

forge-ci release files declare --asset "dist/linux-amd64/${name}-linux-amd64"

printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
