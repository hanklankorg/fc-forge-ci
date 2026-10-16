#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

# Trusted GitLab orchestration. The project must select a pinned external CI
# policy. Only "Check: release request", "Prepare: changelog", "Prepare:
# version" and "Build: release files" execute consumer code. Native artifacts carry data; they are never dotenv reports or sourced.

rc_engine() { "${FORGE_CI_BIN:?}" "$@"; }

# The path guards refuse explicitly. A bare `test -f x && test ! -L x` stops
# nothing under set -e when its first test fails, so the guards say what is
# wrong and return 1, which stops the job.
rc_require_regular_file() {
	if [ ! -f "$1" ] || [ -L "$1" ]; then
		echo "ERROR: $1 must be a regular file, not absent or a link." >&2
		return 1
	fi
}

rc_require_absent() {
	if [ -e "$1" ] || [ -L "$1" ]; then
		echo "ERROR: $1 must not exist yet; it is reserved for this job." >&2
		return 1
	fi
}

# rc_job_role maps a job's displayed name to the stable role the secret policy
# is written in, so wording a name for its reader never moves a secret. A job
# not listed has no role and may hold no release secret.
rc_job_role() {
	case "$1" in
	"Check: signing access") echo signing-preflight ;;
	"Check: publishing access") echo publish-preflight ;;
	"Check: registry access") echo registry-preflight ;;
	"Prepare: release commit and tag") echo prepare ;;
	"Sign: files and images") echo sign ;;
	"Publish: images") echo promote ;;
	"Publish: release") echo publish ;;
	"Publish: tidy up") echo cleanup ;;
	*) echo none ;;
	esac
}

rc_release_context() {
	local name allowed role
	role=$(rc_job_role "${CI_JOB_NAME:?}")
	for name in RELEASE_SSH_SIGNING_KEY RELEASE_COSIGN_PRIVATE_KEY RELEASE_COSIGN_PASSPHRASE \
		RELEASE_GPG_PRIVATE_KEY RELEASE_GPG_PASSPHRASE RELEASE_TOKEN RELEASE_REGISTRY_TOKEN \
		SSH_SIGNING_KEY COSIGN_KEY COSIGN_PASSWORD GPG_SIGNING_KEY GPG_SIGNING_PASSWORD GITLAB_TOKEN; do
		allowed=false
		case "${role}:${name}" in
		prepare:RELEASE_SSH_SIGNING_KEY | sign:RELEASE_COSIGN_PRIVATE_KEY | \
			sign:RELEASE_COSIGN_PASSPHRASE | sign:RELEASE_GPG_PRIVATE_KEY | \
			sign:RELEASE_GPG_PASSPHRASE | publish:RELEASE_TOKEN | \
			signing-preflight:RELEASE_COSIGN_PRIVATE_KEY | signing-preflight:RELEASE_COSIGN_PASSPHRASE | \
			signing-preflight:RELEASE_GPG_PRIVATE_KEY | signing-preflight:RELEASE_GPG_PASSPHRASE | \
			publish-preflight:RELEASE_TOKEN | registry-preflight:RELEASE_REGISTRY_TOKEN | \
			signing-preflight:RELEASE_REGISTRY_TOKEN | sign:RELEASE_REGISTRY_TOKEN | \
			promote:RELEASE_REGISTRY_TOKEN | cleanup:RELEASE_REGISTRY_TOKEN) allowed=true ;;
		esac
		if [ "${allowed}" = false ] && [ -n "${!name:-}" ]; then
			echo "ERROR: ${name} is available to the wrong release job." >&2
			return 1
		fi
	done
	export RELEASE_TAG="${CI_COMMIT_TAG#release-request/}"
	export RELEASE_VERSION="${RELEASE_TAG#v}"
	export RELEASE_REQUEST_SHA="${CI_COMMIT_SHA:?}"
	# The one branch the release lands on: the release-branch input, else the
	# project's default branch. Every engine call that reads a branch is given
	# it explicitly, so the head check and the push target cannot disagree.
	export RC_RELEASE_BRANCH="${RC_RELEASE_BRANCH_INPUT:-${CI_DEFAULT_BRANCH:?}}"
	if ! git check-ref-format --branch "${RC_RELEASE_BRANCH}" >/dev/null 2>&1; then
		echo "ERROR: release branch is not a valid branch name: ${RC_RELEASE_BRANCH}" >&2
		return 1
	fi
	rc_engine version derive-release --ref-name "${CI_COMMIT_TAG}" --require-release-request --require-stable
}

rc_checkout() {
	local ref=$1 format=sha1
	if [ "${#RELEASE_REQUEST_SHA}" -eq 64 ]; then format=sha256; fi
	rc_engine platform checkout \
		--workspace "${RC_WORK}/consumer" --server-url "${CI_SERVER_URL:?}" \
		--repository "${CI_PROJECT_PATH:?}" --ref "${ref}" --object-format "${format}" --fetch-all-refs
	cd -- "${RC_WORK}/consumer" || return
}

rc_request_checkout() {
	rc_checkout "${RELEASE_REQUEST_SHA}"
	printf '%s\n' "${RC_REQUEST_ALLOWED_SIGNERS:?}" >"${RC_WORK}/request-allowed-signers"
	rc_engine release validate-request --release-request "${CI_COMMIT_TAG}" --tag "${RELEASE_TAG}" \
		--expected-commit "${RELEASE_REQUEST_SHA}" --allowed-signers-file "${RC_WORK}/request-allowed-signers"
	rc_engine validate tag commit --tag "${CI_COMMIT_TAG}" --branch "${RC_RELEASE_BRANCH}" --require-head
}

rc_release_checkout() {
	rc_checkout "refs/tags/${RELEASE_TAG}"
	export RELEASE_SHA
	RELEASE_SHA=$(git rev-parse HEAD)
	# The release commit is the tested request plus the changelog, and with a
	# version bump the verified version substitution: nothing else is built.
	local -a bump_args=()
	if [ -n "${RC_VERSION_BUMP:-}" ]; then
		bump_args=(--version-bump "${RC_VERSION_BUMP}")
	fi
	rc_engine release validate-tag --release-sha "${RELEASE_SHA}" --tag "${RELEASE_TAG}" \
		--expected-parent "${RELEASE_REQUEST_SHA}" --changelog-only "${bump_args[@]}" \
		--repo-url "${CI_SERVER_URL}/${CI_PROJECT_PATH}.git"
}

rc_render_changelog() {
	rc_request_checkout
	# The renderer and consumer configuration stay in this key-free job.
	# shellcheck source=.forgejo/actions/render-changelog/versions.env
	source "${RC_HELPERS_DIR}/.forgejo/actions/render-changelog/versions.env"
	local version amd64 arm64
	case "${RC_CHANGELOG_BACKEND}" in
	git-chglog)
		version=${GIT_CHGLOG_VERSION}
		amd64=${GIT_CHGLOG_LINUX_X64_SHA256}
		arm64=${GIT_CHGLOG_LINUX_ARM64_SHA256}
		;;
	git-cliff)
		version=${GIT_CLIFF_VERSION}
		amd64=${GIT_CLIFF_LINUX_X64_SHA256}
		arm64=${GIT_CLIFF_LINUX_ARM64_SHA256}
		;;
	*) return 1 ;;
	esac
	rc_engine toolchain install-changelog-renderer --backend "${RC_CHANGELOG_BACKEND}" \
		--version "${version}" --linux-x64-sha256 "${amd64}" --linux-arm64-sha256 "${arm64}" --path-file "${RC_WORK}/path"
	export PATH="${HOME}/.local/bin:${PATH}"
	rc_engine version render-changelog --backend "${RC_CHANGELOG_BACKEND}" --tag "${RELEASE_TAG}" \
		--branch "${RC_RELEASE_BRANCH}" \
		--repository-url "${CI_PROJECT_URL:?}" --changelog-config "${RC_CHANGELOG_CONFIG}" \
		--commit-body-config "${RC_COMMIT_BODY_CONFIG}"
	local handoff="${CI_PROJECT_DIR}/_release-render"
	mkdir -p -- "${handoff}"
	local file
	for file in CHANGELOG.md commit-msg.txt; do
		rc_require_regular_file "${file}"
		cp -- "${file}" "${handoff}/${file}"
	done
}

# rc_bump_version runs the project's build tool to set the release version and
# hands over only the patch and its digest. The job holds no credential.
rc_bump_version() {
	rc_request_checkout
	rc_engine version bump --project-type "${RC_VERSION_BUMP:?}" --version "${RELEASE_VERSION:?}" --working-dir . \
		--maven-cli-opts '--batch-mode --no-transfer-progress --errors --fail-at-end'
	local handoff="${CI_PROJECT_DIR}/_release-bump"
	mkdir -p -- "${handoff}"
	# Literal paths and plain text whatever the project's Git configuration says.
	git -c core.quotePath=true diff --binary --no-ext-diff --no-textconv --no-color --no-renames \
		--src-prefix=a/ --dst-prefix=b/ >"${handoff}/version-bump.patch"
	test -s "${handoff}/version-bump.patch"
	sha256sum -- "${handoff}/version-bump.patch" | cut -d' ' -f1 >"${handoff}/digest"
}

rc_prepare_release() {
	rc_request_checkout
	local file
	local -a include=()
	git -c core.hooksPath=/dev/null checkout -b "${RC_RELEASE_BRANCH}"
	for file in CHANGELOG.md commit-msg.txt; do
		rc_require_regular_file "${CI_PROJECT_DIR}/_release-render/${file}"
		cp --remove-destination -- "${CI_PROJECT_DIR}/_release-render/${file}" "${file}"
	done
	# Text checks only, no build tool: the patch matches its digest, touches only
	# the project files of the type, and moves the version and nothing else.
	if [ -n "${RC_VERSION_BUMP:-}" ]; then
		local bump="${CI_PROJECT_DIR}/_release-bump" patch_digest
		for file in version-bump.patch digest; do
			rc_require_regular_file "${bump}/${file}"
		done
		patch_digest=$(head -c 64 -- "${bump}/digest")
		rc_engine version apply-bump --project-type "${RC_VERSION_BUMP}" --version "${RELEASE_TAG}" --working-dir . \
			--patch "${bump}/version-bump.patch" --sha256 "${patch_digest}" --paths-file "${RC_WORK:?}/bump-paths"
		include=(--include-paths-file "${RC_WORK}/bump-paths")
	fi
	SSH_SIGNING_KEY="${RELEASE_SSH_SIGNING_KEY:?}" rc_engine version commit-changelog-release \
		--tag "${RELEASE_TAG}" --branch "${RC_RELEASE_BRANCH}" --repository "${CI_PROJECT_PATH}" --git-url "${RC_GIT_URL:?}" \
		--host-key-fingerprint "${RC_GIT_HOST_KEY:?}" --author-name "${RC_BOT_NAME:?}" --author-email "${RC_BOT_EMAIL:?}" \
		"${include[@]}"
}

rc_build_handoff() {
	if [ -e dist/base-images.json ]; then test "${RC_BASE_IMAGES}" = true; fi
	rc_engine release files validate --unsigned --manifest dist/release-files.json --dist-dir dist
	rc_engine release prepare-dist --path dist --prune-dirs false
	local handoff="${CI_PROJECT_DIR}/_release-build"
	mkdir -p -- "${handoff}"
	cp -R -- dist "${handoff}/dist"
	rc_engine release dist-digest --dist-dir "${handoff}/dist" --manifest-root . >"${handoff}/digest"
}

rc_read_handoff() {
	local directory=$1 digest
	rc_require_regular_file "${directory}/digest"
	digest=$(<"${directory}/digest")
	[[ "${digest}" =~ ^[0-9a-f]{64}$ ]]
	rc_engine release validate-dist --dist-dir "${directory}/dist" --expected-digest "${digest}" --manifest-root .
	# Preserve the manifest's dist/... paths in a private checkout, not in the
	# runner's artifact extraction directory. Consumer files cannot preseed it.
	rc_require_absent dist
	cp -R -- "${directory}/dist" dist
	export DIST_DIR=dist RELEASE_FILES_MANIFEST=dist/release-files.json
}

rc_public_key() {
	local key=${COSIGN_PUBLIC_KEY:?}
	printf '%s\n' "${key%$'\n'}" >"${RC_WORK}/cosign.pub"
	printf '%s\n' "${RC_PUBLIC_KEY_SHA256:?}" >"${RC_WORK}/cosign.pub.sha256"
	rc_engine platform stage-public-key --source "${RC_WORK}/cosign.pub" \
		--digest-file "${RC_WORK}/cosign.pub.sha256" --workspace "${RC_WORK}" --destination trusted.pub
}

rc_signing_preflight() {
	SIGNER_STRICT=1 bash "${RC_HELPERS_DIR}/.forgejo/scripts/verify-signer-toolchain.sh"
	rc_public_key
	export COSIGN_KEY="${RELEASE_COSIGN_PRIVATE_KEY:?}" COSIGN_PASSWORD="${RELEASE_COSIGN_PASSPHRASE:?}"
	rc_engine validate signing-key --key env://COSIGN_KEY --trusted-public-key "${RC_WORK}/trusted.pub" \
		--trusted-public-key-digest "${RC_WORK}/cosign.pub.sha256"
	if [ "${RC_SIGN_DISTRO_PACKAGES}" = true ]; then
		local gpg_required='is required while sign-distro-packages is true; set it, or set sign-distro-packages: false'
		: "${RELEASE_GPG_PRIVATE_KEY:?${gpg_required}}" "${RELEASE_GPG_PASSPHRASE:?${gpg_required}}" \
			"${RELEASE_GPG_FINGERPRINT:?${gpg_required}}"
	fi
	unset COSIGN_KEY COSIGN_PASSWORD
}

rc_sign_release() {
	rc_signing_preflight
	rc_release_checkout
	rc_read_handoff "${CI_PROJECT_DIR}/_release-build"
	rc_engine release files validate --unsigned --manifest "${RELEASE_FILES_MANIFEST}" --dist-dir "${DIST_DIR}"
	# Only this trusted job may create the publication ledger. A producer must
	# not bypass import by pre-seeding that reserved path without a source ledger.
	rc_require_absent "${DIST_DIR}/release-images-publication.json"
	rc_require_absent "${DIST_DIR}/base-images-publication.json"
	if [ -f "${DIST_DIR}/release-images.json" ]; then
		rc_publication_scope
		REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
			rc_engine container release-images import --dist-dir "${DIST_DIR}" --ledger "${DIST_DIR}/release-images.json" \
			--tag "${RELEASE_TAG}" --registry "${CI_REGISTRY}" --server-url "${CI_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
			--expected-image-repository "${CI_REGISTRY_IMAGE}" --destination-repository "${RC_PUBLICATION_IMAGE_REPOSITORY:?}" \
			--output-ledger "${DIST_DIR}/release-images-publication.json"
	fi
	export COSIGN_KEY="${RELEASE_COSIGN_PRIVATE_KEY:?}" COSIGN_PASSWORD="${RELEASE_COSIGN_PASSPHRASE:?}"
	rc_sign_bases
	if [ "${RC_SIGN_DISTRO_PACKAGES}" = true ]; then
		GPG_SIGNING_KEY="${RELEASE_GPG_PRIVATE_KEY:?}" GPG_SIGNING_PASSWORD="${RELEASE_GPG_PASSPHRASE:?}" \
			rc_engine release gpg sign-packages --fingerprint "${RELEASE_GPG_FINGERPRINT:?}" --dir "${DIST_DIR}"
	fi
	rc_engine release sign --method kms --key env://COSIGN_KEY --checksums-from-manifest \
		--manifest "${RELEASE_FILES_MANIFEST}" --dist-dir "${DIST_DIR}" --manifest-section sboms --no-release-artifacts-dir
	rc_engine release provenance --checksums-from-manifest --manifest "${RELEASE_FILES_MANIFEST}" \
		--dist-dir "${DIST_DIR}" --go-sum go.sum --workflow gitlab/release.yml \
		--ref-name "${RELEASE_TAG}" --source-sha "${RELEASE_SHA}" --started-on-commit "${RELEASE_SHA}" \
		--output "${DIST_DIR}/slsa-provenance.intoto.json" --method kms --key env://COSIGN_KEY \
		--bundle "${DIST_DIR}/slsa-provenance.intoto.json.bundle"
	if [ -f "${DIST_DIR}/release-images-publication.json" ]; then
		local base_repository="${CI_REGISTRY_IMAGE}-base"
		if [ "${RC_BASE_IMAGES}" = true ]; then base_repository="${RC_PUBLICATION_IMAGE_REPOSITORY}/bases"; fi
		REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
			rc_engine container release-images sign --ledger "${DIST_DIR}/release-images-publication.json" --method kms --key env://COSIGN_KEY \
			--server-url "${CI_SERVER_URL}" --repository "${CI_PROJECT_PATH}" --registry "${CI_REGISTRY}" \
			--expected-image-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}" --expected-base-repository "${base_repository}"
	fi
	rc_engine release sign --method kms --key env://COSIGN_KEY --no-checksums-file --no-release-artifacts-dir \
		--manifest "${RELEASE_FILES_MANIFEST}" --dist-dir "${DIST_DIR}" --manifest-section evidence
	unset COSIGN_KEY COSIGN_PASSWORD
	rc_engine release files manifest --manifest "${RELEASE_FILES_MANIFEST}" --dist-dir "${DIST_DIR}"
	local handoff="${CI_PROJECT_DIR}/_release-sign"
	mkdir -p -- "${handoff}"
	cp -R -- "${DIST_DIR}" "${handoff}/dist"
	rc_engine release dist-digest --dist-dir "${handoff}/dist" --manifest-root . >"${handoff}/digest"
}

rc_promote_release() {
	rc_release_checkout
	rc_read_handoff "${CI_PROJECT_DIR}/_release-sign"
	rc_promote_bases
	mkdir -p -- "${CI_PROJECT_DIR}/_release-promote"
	if [ ! -f "${DIST_DIR}/release-images-publication.json" ]; then return; fi
	REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
		rc_engine container release-images promote --ledger "${DIST_DIR}/release-images-publication.json" \
		--server-url "${CI_SERVER_URL}" --repository "${CI_PROJECT_PATH}" --registry "${CI_REGISTRY}" \
		--expected-image-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}" --journal "${CI_PROJECT_DIR}/_release-promote/image-promotions.jsonl"
}

# Bases use their own strict import/sign/verification path. They are data from
# the key-free build, never consumer scripts executed by the signing job.
rc_sign_bases() {
	if [ ! -f "${DIST_DIR}/base-images.json" ]; then return; fi
	test "${RC_BASE_IMAGES}" = true
	rc_publication_scope
	REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
		rc_engine container base-images import --ledger "${DIST_DIR}/base-images.json" \
		--dist-dir "${DIST_DIR}" --source-repository "${CI_REGISTRY_IMAGE}/bases" \
		--expected-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}/bases" \
		--registry "${CI_REGISTRY}" --server-url "${CI_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
		--output-ledger "${DIST_DIR}/base-images-publication.json"
	REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
		rc_engine container base-images sign --ledger "${DIST_DIR}/base-images-publication.json" \
		--expected-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}/bases" \
		--registry "${CI_REGISTRY}" --server-url "${CI_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
		--workflow gitlab/release.yml --source-sha "${RELEASE_SHA}" --started-on "$(git log -1 --format=%cI)" \
		--method kms --key env://COSIGN_KEY
}

rc_publication_scope() {
	test "${RC_PUBLICATION_PROJECT:?}" != "${CI_PROJECT_PATH:?}"
	case "${RC_PUBLICATION_IMAGE_REPOSITORY:?}" in
	"${CI_REGISTRY:?}/${RC_PUBLICATION_PROJECT}" | "${CI_REGISTRY}/${RC_PUBLICATION_PROJECT}/"*) ;;
	*)
		echo 'ERROR: publication images must belong to the separate publication project.' >&2
		return 1
		;;
	esac
}

rc_promote_bases() {
	if [ ! -f "${DIST_DIR}/base-images-publication.json" ]; then return; fi
	test "${RC_BASE_IMAGES}" = true
	rc_public_key
	local images
	# Project the already-admitted ledger into the base promoter's metadata
	# contract. Values remain JSON arguments; none are evaluated or sourced.
	images=$(jq -c '[.[] | {flavor, tag: .final_tag, ref: .ref, candidate_tag: .candidate_tag, candidate_ref: .ref, base_input_id, sbom_sha256}]' "${DIST_DIR}/base-images-publication.json")
	(
		cd -- "${RC_WORK}" || exit
		REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
			rc_engine container base-images promote --all-images-json "${images}" \
			--expected-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}/bases" \
			--registry "${CI_REGISTRY}" --server-url "${CI_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
			--expected-workflow gitlab/release.yml --cosign-public-key-path trusted.pub \
			--cosign-public-key-sha256 "${RC_PUBLIC_KEY_SHA256}"
	)
}

rc_publish_release() {
	rc_release_checkout
	rc_read_handoff "${CI_PROJECT_DIR}/_release-sign"
	GITLAB_TOKEN="${RELEASE_TOKEN:?}" rc_engine release publish --tag "${RELEASE_TAG}" \
		--repository "${CI_PROJECT_PATH}" --release-name-from-repository --draft=false \
		--release-notes-file "${DIST_DIR}/release-notes.md" --manifest "${RELEASE_FILES_MANIFEST}" --dist-dir "${DIST_DIR}"
	# Opt-in retention: older releases keep their checksums and signatures.
	if [ -n "${RC_RELEASE_ASSET_RETENTION:-}" ]; then
		GITLAB_TOKEN="${RELEASE_TOKEN:?}" rc_engine release asset-retention --repository "${CI_PROJECT_PATH}" \
			--keep "${RC_RELEASE_ASSET_RETENTION}" --dry-run=false
	fi
}

rc_cleanup_release() {
	cd -- "${RC_WORK}" || return
	rc_read_handoff "${CI_PROJECT_DIR}/_release-sign"
	if [ ! -f "${DIST_DIR}/release-images-publication.json" ]; then return; fi
	GITLAB_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
		rc_engine container release-images cleanup --ledger "${DIST_DIR}/release-images-publication.json" \
		--server-url "${CI_SERVER_URL}" --repository "${RC_PUBLICATION_PROJECT}" --registry "${CI_REGISTRY}" \
		--expected-image-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}"
}
