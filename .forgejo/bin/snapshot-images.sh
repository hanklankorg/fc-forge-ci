#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

# Shared native snapshot steps. Source only from the pinned layer checkout,
# never from the consumer checkout. The engine owns planning and publication.
#
# Every engine call carries the administrator's settings as flags: the
# artifact builds run the application tests and publication scans images.
# The caller assigns SNAPSHOT_ADMIN_APPLICATION_TESTS and
# SNAPSHOT_ADMIN_VULNERABILITIES from its inputs before any call; unset,
# the call fails rather than fall back to another value.
snapshot_engine() {
	run_forge_ci --enforce-application-tests="${SNAPSHOT_ADMIN_APPLICATION_TESTS:?}" \
		--enforce-vulnerabilities="${SNAPSHOT_ADMIN_VULNERABILITIES:?}" "$@"
}

snapshot_validate_event() {
	case "${FORGE_CI_PROVIDER:-}" in
	gitlab)
		# Pinned snapshot policy only. Read the provider's runner context, not
		# cross-forge event overrides or a caller-controlled ALLOWED_EVENTS.
		# The provider classifies MR children as pull_request before admission.
		snapshot_engine validate event-context --event-name '' \
			--allowed-events push,workflow_dispatch,api,schedule,parent_pipeline
		;;
	*) snapshot_engine validate event-context ;;
	esac
}

snapshot_checkout() {
	local ref=$1
	snapshot_validate_event
	snapshot_engine platform checkout --repository "${SNAPSHOT_REPOSITORY:?}" \
		--server-url "${SNAPSHOT_SERVER:?}" --ref "${ref}" \
		--fetch-tags --path snapshot-source
}

snapshot_plan() {
	git check-ref-format "refs/heads/${SNAPSHOT_BRANCH:?}"
	local prepared=${1:-} ref="refs/heads/${SNAPSHOT_BRANCH}"
	if [ -n "${prepared}" ]; then
		# Forgejo's prebuilt same-run context producer already resolved the
		# branch. Do not resolve it a second time after its artifacts are built.
		test -f "${prepared}" && test ! -L "${prepared}"
		local branch
		branch=$(snapshot_engine container snapshot-images read-plan --plan "${prepared}" --field source-branch) || return
		test "${branch}" = "${SNAPSHOT_BRANCH}"
		ref=$(snapshot_engine container snapshot-images read-plan --plan "${prepared}" --field source-sha) || return
		[[ "${ref}" =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]]
	fi
	snapshot_checkout "${ref}"
	local sha
	sha=$(git -C snapshot-source rev-parse HEAD)
	(
		cd snapshot-source || exit
		snapshot_engine container snapshot-images plan \
			--file "${SNAPSHOT_CONFIG:-.forge-ci/artifacts.yml}" \
			--branch "${SNAPSHOT_BRANCH}" --source-sha "${sha}" \
			--image-repository "${SNAPSHOT_IMAGE_REPOSITORY:?}" \
			--publish="${SNAPSHOT_PUBLISH:-true}" --output ../snapshot-plan.json
	)
}

# A context producer in THIS run supplies the frozen plan and the inventory
# recorded by the engine after its build. Never regenerate that inventory from
# downloaded bytes. Restore validates and stages only planned dependency files.
snapshot_apply_context() {
	local context plan
	context=$(realpath -e -- "$1") || return
	plan=$(realpath -e -- "$2") || return
	(
		cd snapshot-source || exit
		snapshot_engine container snapshot-images restore-context --plan "${plan}" --input "${context}"
	)
}

snapshot_build_artifact() {
	local name=$1
	(
		cd snapshot-source || exit
		# Producer tools run in a read-only job, without registry/signing auth.
		unset FORGEJO_TOKEN GITHUB_TOKEN CI_JOB_TOKEN CI_REGISTRY_PASSWORD REGISTRY_TOKEN REGISTRY_PASSWORD SNAPSHOT_REGISTRY_PASSWORD
		unset RELEASE_TOKEN RELEASE_GPG_PRIVATE_KEY RELEASE_GPG_PASSPHRASE RELEASE_SSH_SIGNING_KEY
		unset RELEASE_COSIGN_PRIVATE_KEY RELEASE_COSIGN_PASSPHRASE COSIGN_KEY COSIGN_PASSWORD FORGE_CI_BUILD_SECRETS_JSON CI_DEPLOY_PASSWORD CI_DEPENDENCY_PROXY_PASSWORD
		snapshot_engine container snapshot-images build-artifact --plan ../snapshot-plan.json \
			--name "${name}" --output-dir _snapshot-artifacts
	)
}

snapshot_gather_context() {
	local input=${1:-_snapshot-artifacts}
	(
		cd snapshot-source || exit
		mkdir -p "${input}"
		snapshot_engine container snapshot-images gather-context --plan ../snapshot-plan.json \
			--input-dir "${input}" --output snapshot-context
	)
}

snapshot_setup_builder() (
	local builder=$1 arch=$2 auth_dir=''
	local -a auth=()
	trap 'if [ -n "$auth_dir" ]; then rm -rf -- "$auth_dir"; fi' EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM HUP
	if [ "$builder" = kaniko ]; then
		# A private GitLab registry refuses an anonymous read; the job token
		# reads what the runner pulled the job image with. Its destination is
		# the policy's readonly literal, never the overridable CI_REGISTRY.
		if [ "${FORGE_CI_PROVIDER:-}" = gitlab ] && [ -n "${RC_REGISTRY:-}" ] && [ -n "${CI_JOB_TOKEN:-}" ]; then
			auth_dir=$(mktemp -d) || return
			auth=(--auth-file "${auth_dir}/auth.json")
			printf '%s' "${CI_JOB_TOKEN}" | snapshot_engine container login --registry "${RC_REGISTRY}" \
				--registry-username gitlab-ci-token --registry-password-file - "${auth[@]}" || return
		fi
	fi
	snapshot_engine container setup-image-builder --image-builder "$builder" \
		--job-image "${CI_IMAGE_BUILDER_JOB_IMAGE:-}" --engine-repository "${FORGE_CI_REPOSITORY_URL:-}" \
		--install-packages=true --probe-build=true --print-store=false --summary=false "${auth[@]}" --arch "$arch"
)

snapshot_build() {
	local name=$1 arch=$2 builder=${CI_IMAGE_BUILDER:-buildah}
	snapshot_setup_builder "$builder" "$arch" || return
	(
		cd snapshot-source || exit
		# Checkout and artifact reads have finished. No registry or release
		# credential is passed into container build execution.
		unset FORGEJO_TOKEN GITHUB_TOKEN CI_JOB_TOKEN CI_REGISTRY_PASSWORD REGISTRY_TOKEN REGISTRY_PASSWORD SNAPSHOT_REGISTRY_PASSWORD
		unset RELEASE_TOKEN RELEASE_GPG_PRIVATE_KEY RELEASE_GPG_PASSPHRASE RELEASE_SSH_SIGNING_KEY
		unset RELEASE_COSIGN_PRIVATE_KEY RELEASE_COSIGN_PASSPHRASE COSIGN_KEY COSIGN_PASSWORD FORGE_CI_BUILD_SECRETS_JSON CI_DEPLOY_PASSWORD CI_DEPENDENCY_PROXY_PASSWORD
		snapshot_engine container snapshot-images build --plan ../snapshot-plan.json \
			--name "${name}" --arch "${arch}" --metadata-dir snapshot-layouts \
			--image-builder "${builder}" --server-url "${SNAPSHOT_SERVER}" --repository "${SNAPSHOT_REPOSITORY}"
	)
}

snapshot_evidence_tools() {
	local name=$1 sbom
	sbom=$(snapshot_engine container snapshot-images read-plan --plan snapshot-plan.json --name "${name}" --field sbom) || return
	# Every published image is scanned; the administrator policy in the engine
	# decides whether findings block.
	# shellcheck source=scripts/bootstrap/install-trivy.sh
	source "${SNAPSHOT_HELPERS:?}/scripts/bootstrap/install-trivy.sh"
	install_trivy
	if [ "${sbom}" = true ]; then
		# shellcheck source=scripts/bootstrap/install-syft.sh
		source "${SNAPSHOT_HELPERS:?}/scripts/bootstrap/install-syft.sh"
		install_syft
	fi
}

snapshot_publish() {
	local name=$1 layouts=$2 password=${SNAPSHOT_REGISTRY_PASSWORD:?snapshot publisher requires a registry password}
	mkdir -p snapshot-receipts
	# Password is stdin, not argv or a plan member. The engine isolates auth.
	printf '%s' "${password}" | snapshot_engine container snapshot-images publish \
		--plan snapshot-plan.json --name "${name}" --metadata-dir "${layouts}" \
		--registry-username "${SNAPSHOT_REGISTRY_USER:?}" --registry-password-file - \
		--server-url "${SNAPSHOT_SERVER}" --repository "${SNAPSHOT_REPOSITORY}" \
		--output "snapshot-receipts/${name}.json" --evidence-dir "snapshot-evidence/${name}"
}

snapshot_collect() {
	mkdir -p snapshot-receipts
	snapshot_engine container snapshot-images collect --plan snapshot-plan.json \
		--receipts-dir snapshot-receipts --output container-images.json
}
