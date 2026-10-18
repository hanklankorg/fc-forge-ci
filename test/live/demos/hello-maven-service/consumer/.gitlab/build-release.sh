#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release build, called by the pinned release policy in a key-free job. It
# leaves the unsigned hand-off in dist/: the jar, its CycloneDX Build SBOM, the
# ledger of the runtime image it pushed for staging, release notes, checksums
# and the manifest. Forge CI's signer jobs sign and publish it. The policy's
# version-bump: maven put the release version into the release commit; the
# build requires it there.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"

maven_opts=(--batch-mode --no-transfer-progress --errors)
BUILD_TYPE=app MAVEN_CLI_OPTS="${maven_opts[*]}" CYCLONEDX_MAVEN_VERSION=2.9.1 JAVA_VERSION=25 \
	forge-ci build maven run --expect-version "${RELEASE_VERSION}"

jar="target/hello-maven-service-${RELEASE_VERSION}.jar"
test -f "${jar}"
mkdir -p dist
cp -- "${jar}" dist/
cp -- target/bom.json "dist/hello-maven-service-${RELEASE_VERSION}.cdx.json"

if ! forge-ci release notes --changelog-file CHANGELOG.md \
	--release-version "${RELEASE_TAG}" --target-file dist/release-notes.md; then
	forge-ci release notes --source-file /dev/null/absent --target-file dist/release-notes.md \
		--release-version "${RELEASE_TAG}" --release-commit "${RELEASE_SHA}"
fi
# The runtime image goes to this project's registry as an untrusted staging
# image; the signing jobs import it into the publication project. The job
# token pushes it, from an auth file that lives only in this job.
auth_dir=$(mktemp -d)
trap 'rm -rf -- "${auth_dir}"' EXIT
printf '%s' "${CI_JOB_TOKEN:?}" | forge-ci container login --registry "${CI_REGISTRY:?}" \
	--registry-username gitlab-ci-token --registry-password-file - --auth-file "${auth_dir}/auth.json"
CI_OUTPUT="${auth_dir}/image.env" forge-ci container build-push-oci-image \
	--tag "${RELEASE_TAG}" --containerfile Containerfile --context . \
	--builds-json '[{"platform":"linux/amd64"}]' --image "${CI_REGISTRY_IMAGE:?}" \
	--title hello-maven-service --auth-file "${auth_dir}/auth.json" \
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

forge-ci release files declare \
	--asset 'dist/*.jar' \
	--sbom "dist/hello-maven-service-${RELEASE_VERSION}.cdx.json"

printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
