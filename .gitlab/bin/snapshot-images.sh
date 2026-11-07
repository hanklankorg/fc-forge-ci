#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

# Native GitLab transport only. The Go owners plan and admit the child graph.
snapshot_gitlab_checkout() {
	local sha
	sha=$(snapshot_engine container snapshot-images read-plan --plan snapshot-plan.json --field source-sha) || return
	snapshot_checkout "${sha}"
}

snapshot_gitlab_handoff() {
	if [ -e "$2" ] || [ -L "$2" ]; then
		echo "ERROR: $2 must not exist yet; it is reserved for this job's hand-off." >&2
		return 1
	fi
	mv -- "$1" "$2"
}

snapshot_gitlab_build() {
	local name=${1:?} arch=${2:?} context=${3-}
	snapshot_gitlab_checkout
	if [ -n "${context}" ]; then
		snapshot_apply_context _snapshot-context snapshot-plan.json
	else
		snapshot_apply_context snapshot-context snapshot-plan.json
	fi
	snapshot_build "${name}" "${arch}"
	# Kaniko deletes native tools; use the surviving /kaniko toolset.
	hash -r
	snapshot_gitlab_handoff snapshot-source/snapshot-layouts snapshot-layouts || return
	# after_script has no shell after Kaniko; prepare uploader access here.
	find snapshot-layouts \( -type d -o -type f \) -exec chmod u=rwX,go=rX -- {} +
}

snapshot_gitlab_produce() {
	local name=${1:?}
	snapshot_gitlab_checkout
	_forge_ci_arch
	test "${HELPERS_ENGINE_ARCH}" = amd64
	snapshot_build_artifact "${name}"
	snapshot_gitlab_handoff snapshot-source/_snapshot-artifacts _snapshot-artifacts || return
}

snapshot_gitlab_gather() {
	snapshot_gitlab_checkout
	mkdir -p _snapshot-artifacts
	snapshot_gather_context "$(realpath -e _snapshot-artifacts)"
	snapshot_gitlab_handoff snapshot-source/snapshot-context snapshot-context || return
}

snapshot_gitlab_publish() {
	local name=${1:?}
	# Checkout reads scanner policy only; no source commands execute.
	snapshot_gitlab_checkout
	if [ -f snapshot-source/.trivyignore.yaml ]; then cp snapshot-source/.trivyignore.yaml .; fi
	snapshot_evidence_tools "${name}"
	SNAPSHOT_REGISTRY_USER=${SNAPSHOT_REGISTRY_USER:-${CI_REGISTRY_USER:-}}
	SNAPSHOT_REGISTRY_PASSWORD=${SNAPSHOT_REGISTRY_PASSWORD:-${CI_REGISTRY_PASSWORD:-}}
	snapshot_publish "${name}" snapshot-layouts
}

snapshot_gitlab_generate() {
	snapshot_engine plan gitlab-snapshot-pipeline --plan "$1" --output "$2" \
		--project "${SNAPSHOT_LAYER_PROJECT:?}" --engine-sha "${SNAPSHOT_ENGINE_SHA:?}" \
		--server-url "${RC_SERVER_URL:?}" --registry "${RC_REGISTRY?}" --pipeline-id "${CI_PIPELINE_ID:?}" \
		--context-job "${SNAPSHOT_CONTEXT_JOB:-}" --job-image "${SNAPSHOT_JOB_IMAGE:?}" --build-image "${SNAPSHOT_BUILD_IMAGE:?}" \
		--image-builder "${CI_IMAGE_BUILDER:-buildah}" --builder-job-image "${CI_IMAGE_BUILDER_JOB_IMAGE:-}" \
		--runtime-image-go "${SNAPSHOT_ARTIFACT_IMAGE_GO:-${SNAPSHOT_JOB_IMAGE}}" \
		--runtime-image-java "${SNAPSHOT_ARTIFACT_IMAGE_JAVA:-${SNAPSHOT_JOB_IMAGE}}" \
		--runtime-image-node "${SNAPSHOT_ARTIFACT_IMAGE_NODE:-${SNAPSHOT_JOB_IMAGE}}" \
		--runtime-image-rust "${SNAPSHOT_ARTIFACT_IMAGE_RUST:-${SNAPSHOT_JOB_IMAGE}}" \
		--runner-tags "${SNAPSHOT_RUNNER_TAGS:-[]}" --amd64-tags "${SNAPSHOT_AMD64_TAGS:-[]}" --arm64-tags "${SNAPSHOT_ARM64_TAGS:-[]}" \
		--enforce-application-tests="${SNAPSHOT_ADMIN_APPLICATION_TESTS:?}" --enforce-vulnerabilities="${SNAPSHOT_ADMIN_VULNERABILITIES:?}"
}
