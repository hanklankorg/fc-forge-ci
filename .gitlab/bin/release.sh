#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

# Trusted GitLab orchestration. The project must select a pinned external CI
# policy. Only "Check: consumer checks", "Prepare: changelog", "Prepare:
# version" and "Build: release files" execute consumer code, and the "Images:
# build" jobs the consumer's Containerfiles, with the job token alone. Native
# artifacts carry data; they are never dotenv reports or sourced.
#
# The policy's values, RC_* and FORGE_CI_COSIGN_TRANSPARENCY, are the
# readonly literals the job's start assigns from its inputs; a project,
# group or instance variable of the same name never reaches them. The
# other names read here are GitLab's predefined variables, the release
# secrets and what the start or these functions set.

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

# rc_new_handoff creates a producer's hand-off directory. A job that ran
# consumer code may have left one in the workspace; it is refused, never
# merged into.
rc_new_handoff() {
	rc_require_absent "$1" && mkdir -- "$1"
}

# rc_release_context holds each release secret to the roles the policy
# gives it. The role is rc_job_role, which the job's start sets as a literal
# of the template: a job's name, like any GitLab variable, a project can set.
# A role not listed may hold no release secret.
rc_release_context() {
	local name allowed role=${rc_job_role:?}
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
	export RELEASE_POLICY="${RC_RELEASE_POLICY:?}"
	rc_engine version derive-release --ref-name "${CI_COMMIT_TAG}" --require-release-request
}

rc_checkout() {
	local ref=$1 format=sha1
	if [ "${#RELEASE_REQUEST_SHA}" -eq 64 ]; then format=sha256; fi
	rc_engine platform checkout \
		--workspace "${RC_WORK}/consumer" --server-url "${RC_SERVER_URL}" \
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
		--repo-url "${RC_SERVER_URL}/${CI_PROJECT_PATH}.git"
}

# rc_image_builder prepares the image job's builder before any Containerfile
# is built. The job runs in builder-job-image, whichever the builder. Buildah
# is set up as the snapshot builds set it up, from what the image carries:
# nothing is installed at run time. kaniko's image is admitted as the
# snapshot builds admit it: by the kaniko job image's title and the engine
# project it was built from.
rc_image_builder() (
	local -a auth_args=()
	local auth_dir=''
	trap 'if [ -n "$auth_dir" ]; then rm -rf -- "$auth_dir"; fi' EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM HUP
	# The job token reads what the runner pulled the job's image with.
	if [ "${CI_IMAGE_BUILDER:-buildah}" = kaniko ] && [ -n "${RC_REGISTRY:-}" ]; then
		auth_dir=$(mktemp -d "${RC_WORK:?}/builder-auth.XXXXXX") || return
		auth_args=(--auth-file "${auth_dir}/auth.json")
		printf '%s' "${CI_JOB_TOKEN:?}" | rc_engine container login --registry "${RC_REGISTRY}" \
			--registry-username gitlab-ci-token --registry-password-file - "${auth_args[@]}" || return
	fi
	rc_engine container setup-image-builder --image-builder "${CI_IMAGE_BUILDER:-buildah}" \
		--job-image "${CI_IMAGE_BUILDER_JOB_IMAGE:-}" --engine-repository "${FORGE_CI_REPOSITORY_URL:?}" \
		--require-job-image --install-packages=false --probe-build=true --print-store=false --summary=false "${auth_args[@]}"
)

rc_render_changelog() {
	rc_request_checkout
	# The renderer and consumer configuration stay in this key-free job.
	# shellcheck source=.forgejo/actions/render-changelog/versions.env
	source "${RC_HELPERS_DIR}/.forgejo/actions/render-changelog/versions.env"
	local version amd64 arm64
	case "${RC_CHANGELOG_CREATOR}" in
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
	rc_engine toolchain install-changelog-renderer --changelog-creator "${RC_CHANGELOG_CREATOR}" \
		--version "${version}" --linux-x64-sha256 "${amd64}" --linux-arm64-sha256 "${arm64}" --path-file "${RC_WORK}/path"
	export PATH="${HOME}/.local/bin:${PATH}"
	rc_engine version render-changelog --changelog-creator "${RC_CHANGELOG_CREATOR}" --tag "${RELEASE_TAG}" \
		--branch "${RC_RELEASE_BRANCH}" \
		--repository-url "${CI_PROJECT_URL:?}" --changelog-config "${RC_CHANGELOG_CONFIG}" \
		--commit-body-config "${RC_COMMIT_BODY_CONFIG}"
	local handoff="${CI_PROJECT_DIR}/_release-render"
	rc_new_handoff "${handoff}" || return
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
	rc_new_handoff "${handoff}" || return
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
		rc_engine version apply-bump --release-policy "${RELEASE_POLICY:?}" --project-type "${RC_VERSION_BUMP}" --version "${RELEASE_TAG}" --working-dir . \
			--patch "${bump}/version-bump.patch" --sha256 "${patch_digest}" --paths-file "${RC_WORK:?}/bump-paths"
		include=(--include-paths-file "${RC_WORK}/bump-paths")
	fi
	# Left out, the engine pushes to this project on the web host.
	local git_url=()
	if [[ -n "${RC_GIT_URL:-}" ]]; then
		git_url=(--git-url "${RC_GIT_URL}")
	fi
	SSH_SIGNING_KEY="${RELEASE_SSH_SIGNING_KEY:?}" rc_engine version commit-changelog-release \
		--tag "${RELEASE_TAG}" --branch "${RC_RELEASE_BRANCH}" --repository "${CI_PROJECT_PATH}" "${git_url[@]}" \
		--host-key-fingerprint "${RC_GIT_HOST_KEY:?}" --author-name "${RC_BOT_NAME:?}" --author-email "${RC_BOT_EMAIL:?}" \
		"${include[@]}"
}

rc_build_handoff() {
	if [ -e dist/base-images.json ]; then test "${RC_BASE_IMAGES}" = true; fi
	# Release images are built and recorded by the image jobs alone; a build
	# that pushed its own has nothing the signer may import.
	if [ -e dist/release-images.json ] || [ -L dist/release-images.json ]; then
		echo 'ERROR: dist/release-images.json is written by the image jobs; declare the images under containers: in artifacts.yml, set release-images: true, and leave their build context in image-context/.' >&2
		return 1
	fi
	rc_engine release files validate --unsigned --manifest dist/release-files.json --dist-dir dist
	rc_engine release prepare-dist --path dist --prune-dirs false
	local handoff="${CI_PROJECT_DIR}/_release-build"
	rc_new_handoff "${handoff}" || return
	cp -R -- dist "${handoff}/dist"
	rc_engine release dist-digest --dist-dir "${handoff}/dist" --manifest-root . >"${handoff}/digest"
	# Files the Containerfiles copy that are not in the repository, laid over
	# the release checkout before each image build, as Forgejo's context-artifact.
	if [ -e image-context ] || [ -L image-context ]; then
		if [ ! -d image-context ] || [ -L image-context ]; then
			echo 'ERROR: image-context must be a directory, not a link.' >&2
			return 1
		fi
		cp -R -- image-context "${handoff}/image-context"
	fi
}

# The export jobs: the consumer's export commands, once per selected
# architecture, natively, in builder-job-image with image-builder.

# rc_check_exports refuses, at the release request, an export the jobs would
# not carry out: commands without export-files, an export without
# builder-job-image, or one for arm64 without its runner. The export
# commands arrive on stdin as the interpolated input, data only.
rc_check_exports() {
	local -a image_plan=()
	if [ -e "${RC_WORK-}/release-images-plan.json" ] || [ -L "${RC_WORK-}/release-images-plan.json" ]; then
		image_plan=(--image-plan "${RC_WORK}/release-images-plan.json")
	fi
	rc_engine plan release-exports --enabled "$1" --job-image "$2" --release-platforms "$3" \
		--arm64-tags "$4" "${image_plan[@]}" --commands-file -
}

# rc_export_selected prints a Go-admitted selection; errors must not mean skip.
rc_export_selected() {
	rc_engine plan release-exports --selected-arch "$1" --release-platforms "$2"
}

# rc_export_prepare admits the builder, checks the release commit out and
# drops every credential of the job before the consumer's commands run.
rc_export_prepare() {
	export EXPORT_ARCH=$1 EXPORT_PLATFORM="linux/$1"
	export CI_IMAGE_BUILDER=$2 CI_IMAGE_BUILDER_JOB_IMAGE=$3
	if [ "${HELPERS_ENGINE_ARCH:?}" != "${EXPORT_ARCH}" ]; then
		echo "ERROR: the ${EXPORT_ARCH} export runs on a ${HELPERS_ENGINE_ARCH} runner; it builds natively only" >&2
		return 1
	fi
	rc_image_builder || return
	rc_release_checkout || return
	# kaniko deletes the root filesystem after a build; the kaniko job image's
	# tools under /kaniko take over then, as its own PATH has them.
	if [ "${CI_IMAGE_BUILDER}" = kaniko ]; then export PATH="${PATH}:/kaniko/tools:/kaniko"; fi
	unset CI_JOB_TOKEN CI_REGISTRY_PASSWORD CI_DEPLOY_PASSWORD CI_DEPENDENCY_PROXY_PASSWORD
}

# rc_export_handoff hands this architecture's dist/ to the build job.
rc_export_handoff() {
	local handoff="${CI_PROJECT_DIR}/_release-export-$1"
	rc_new_handoff "${handoff}" || return
	rc_engine release gather-exports --source dist --path "${handoff}/dist" || return
	# A kaniko build leaves no shell at the paths after_script is run with;
	# make the hand-off readable for the uploader here.
	find "${handoff}" \( -type d -o -type f \) -exec chmod u=rwX,go=rX -- {} +
}

# rc_gather_exports lays every export job's dist/ into the build's, refusing
# a path two of them, or the checkout, both hold.
rc_gather_exports() {
	rc_engine release gather-exports --enabled "$1" --allow-missing --path dist \
		--source "${CI_PROJECT_DIR}/_release-export-amd64/dist" --source "${CI_PROJECT_DIR}/_release-export-arm64/dist"
}

# The image stage: plan, one native build job per architecture, and one
# publishing job, as the Forgejo release-images workflow runs them. The jobs
# hold the job token alone; their arguments are the policy's literal inputs,
# never project variables.
rc_images_plan() {
	rc_release_checkout
	rc_new_handoff "${CI_PROJECT_DIR}/_release-images-plan" || return
	rc_plan_release_images "$1" "$2" "$3" "${CI_PROJECT_DIR}/_release-images-plan/plan.json"
}

# rc_plan_release_images writes the image jobs' plan of the checkout to
# output, refusing what no image job would carry out.
rc_plan_release_images() {
	local config=$1 platforms=$2 arm64_tags=$3 output=$4 archs=amd64 label labels=()
	if [ "${arm64_tags}" != '[]' ]; then archs=amd64,arm64; fi
	# The labels input as written, one key=value a line; the engine admits
	# each and refuses one that replaces a standard provenance label.
	while IFS= read -r label; do
		if [ -n "${label}" ]; then labels+=(--label "${label}"); fi
	done <<<"${RC_IMAGE_LABELS?}"
	rc_engine container release-images plan --file "${config}" --image-plan-json '' \
		--release-tag "${RELEASE_TAG}" --release-platforms "${platforms}" "${labels[@]}" \
		--image-repository "${RC_REGISTRY_IMAGE:?}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
		--registry-username gitlab-ci-token --runner-archs "${archs}" --output "${output}"
}

# rc_check_release_images refuses, at the release request and before Prepare
# pushes the release commit and tag, a policy whose image stage would stop
# after it: a builder without its job image, a publication project that is
# not separate, or a plan no image job carries out. The arguments are the
# policy's literal inputs. The image jobs plan again at the release commit,
# which differs from the request only by the changelog and version bump.
rc_check_release_images() {
	local images=$1 builder=$2 job_image=$3 config=$4 platforms=$5 arm64_tags=$6
	case "${images}" in
	true) ;;
	false)
		if [ -n "${RC_IMAGE_LABELS?}" ]; then
			echo 'ERROR: the release policy sets labels but release-images is false; set release-images to true or remove labels.' >&2
			return 1
		fi
		return 0
		;;
	*)
		echo "ERROR: release-images must be true or false, got '${images}'" >&2
		return 1
		;;
	esac
	# Either builder's image jobs run in builder-job-image.
	if [ -z "${job_image}" ]; then
		echo "ERROR: release-images builds in builder-job-image; set it to the ${builder} job image pinned by digest" >&2
		return 1
	fi
	rc_publication_scope || return
	rc_plan_release_images "${config}" "${platforms}" "${arm64_tags}" "${RC_WORK:?}/release-images-plan.json"
}

rc_images_build() {
	local arch=$1 context="${CI_PROJECT_DIR}/_release-build/image-context" created
	local handoff="${CI_PROJECT_DIR}/_release-images-${arch}"
	export CI_IMAGE_BUILDER=$2 CI_IMAGE_BUILDER_JOB_IMAGE=$3
	rc_image_builder || return
	# The engine refuses a planned build of another architecture than the
	# runner's: each builds natively. A job with none planned has no work.
	rc_release_checkout
	# The release commit's own time, read before the build's files are laid
	# over the checkout.
	created=$(TZ=UTC0 git show -s --date=format-local:%Y-%m-%dT%H:%M:%SZ --format=%cd HEAD)
	if [ -d "${context}" ] && [ ! -L "${context}" ]; then cp -R -- "${context}/." .; fi
	(
		cd -- "${RC_WORK}" || exit
		# The engine and the build get no credential of the job. That is not
		# a boundary: under kaniko the RUN steps run in this container and
		# can read the job token from the job shell's /proc environment, the
		# trust Build: release files already gives the consumer. The layouts
		# are uploaded by the publishing job. FROM images are pulled
		# anonymously: the job token is not pull-only, so it is not handed to
		# the builder for pulls (docs/gitlab.md).
		unset CI_JOB_TOKEN CI_REGISTRY_PASSWORD CI_DEPLOY_PASSWORD CI_DEPENDENCY_PROXY_PASSWORD
		rc_engine container release-images build --plan "${CI_PROJECT_DIR}/_release-images-plan/plan.json" \
			--arch "${arch}" --image-repository "${RC_REGISTRY_IMAGE:?}" --release-tag "${RELEASE_TAG}" \
			--release-sha "${RELEASE_SHA}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
			--created "${created}" --source-dir consumer --metadata-dir staged --image-builder "${CI_IMAGE_BUILDER}"
	) || return
	# kaniko deletes the root filesystem after its builds; take the tools from
	# /kaniko, which it keeps.
	if [ "${CI_IMAGE_BUILDER}" = kaniko ]; then export PATH="/kaniko/tools:/kaniko:${PATH}"; fi
	rc_new_handoff "${handoff}" || return
	if [ -d "${RC_WORK}/staged" ]; then cp -R -- "${RC_WORK}/staged/." "${handoff}/"; fi
	# A kaniko build leaves no shell at the paths after_script is run with;
	# make the hand-off readable for the uploader here.
	find "${handoff}" \( -type d -o -type f \) -exec chmod u=rwX,go=rX -- {} +
}

# The admitted native publication outputs have a flat dist/<file> mapping.
# Keep this copy boundary shared with inert handoff tests. Base ledgers and
# premade-sboms/ are not supported by this dist-only handoff; the engine refuses
# base plans rather than flattening their canonical signer paths.
rc_collect_release_images() {
	local images=$1 file
	# Admit the complete fan-in before copying any entry.
	for file in "${images}"/*; do
		rc_require_regular_file "${file}" || return 1
		rc_require_absent "${DIST_DIR:?}/${file##*/}" || return 1
	done
	for file in "${images}"/*; do
		cp -- "${file}" "${DIST_DIR}/" || return 1
	done
}

# rc_images_publish scans and pushes every planned image under its staging
# tag in this project's registry with the job token, then joins the ledger and
# its evidence to the build's dist in a hand-off of its own, digested again.
rc_images_publish() {
	local images="${RC_WORK}/images" auth_file="${RC_WORK}/registry-auth.json"
	# The pinned job image must carry Syft. Never download a scanner here.
	command -v syft >/dev/null || { echo 'ERROR: Images: publish requires Syft in job-image.' >&2; return 1; }
	rc_release_checkout
	rc_read_handoff "${CI_PROJECT_DIR}/_release-build" || return
	# shellcheck source=scripts/bootstrap/install-trivy.sh
	source "${RC_HELPERS_DIR}/scripts/bootstrap/install-trivy.sh"
	install_trivy
	printf '%s' "${CI_JOB_TOKEN:?}" | rc_engine container login --registry "${RC_REGISTRY:?}" \
		--registry-username gitlab-ci-token --registry-password-file - --auth-file "${auth_file}"
	rc_engine container release-images publish --enforce-vulnerabilities="${RC_ADMIN_VULNERABILITIES:?}" \
		--plan "${CI_PROJECT_DIR}/_release-images-plan/plan.json" \
		--image-repository "${RC_REGISTRY_IMAGE:?}" --release-tag "${RELEASE_TAG}" --release-sha "${RELEASE_SHA}" \
		--server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
		--layout-source "amd64=${CI_PROJECT_DIR}/_release-images-amd64" \
		--layout-source "arm64=${CI_PROJECT_DIR}/_release-images-arm64" \
		--prepublication-sboms \
		--output-dir "${images}" --auth-file "${auth_file}" || return
	rm -f -- "${auth_file}"
	rc_collect_release_images "${images}" || return
	rc_register_release_image_evidence "${images}" || return
	rc_engine release prepare-dist --path "${DIST_DIR}" --prune-dirs false
	local handoff="${CI_PROJECT_DIR}/_release-images"
	rc_new_handoff "${handoff}" || return
	cp -R -- "${DIST_DIR}" "${handoff}/dist"
	rc_engine release dist-digest --dist-dir "${handoff}/dist" --manifest-root . >"${handoff}/digest"
}

# Enumerate only the trusted publisher's fresh output, never consumer dist.
# Go admission requires the complete ledger-pinned index/platform set and checks
# every byte pin before this manifest can reach dist-digest or the signer.
rc_register_release_image_evidence() {
	local images=$1 file
	local -a evidence=()
	rc_require_regular_file "${DIST_DIR:?}/release-images.json" || return
	for file in "${images}"/image-sbom-*.cyclonedx.json "${images}"/sbom-*.json; do
		[ -e "${file}" ] || continue
		evidence+=(--evidence-file "${DIST_DIR:?}/${file##*/}")
	done
	rc_engine release files manifest --unsigned --manifest "${RELEASE_FILES_MANIFEST:?}" \
		--dist-dir "${DIST_DIR}" --release-images-ledger "${DIST_DIR}/release-images.json" "${evidence[@]}"
}

rc_read_handoff() {
	local directory=$1 mode=${2:-dist}
	# Conditional callers disable errexit. Return explicitly before exposing paths.
	case "${mode}" in
	canonical-payload)
		# Explicit unsigned root-base transport only. No production job selects
		# this mode; signed/publication handoffs retain their separate contract.
		rc_engine release validate-dist --dist-dir "${directory}/payload" --digest-file "${directory}/digest" \
			--canonical-payload --stage-dir verified-payload || return
		export DIST_DIR=verified-payload/dist RELEASE_FILES_MANIFEST=verified-payload/dist/release-files.json
		export RC_EVIDENCE_ROOT=verified-payload
		return
		;;
	dist) ;;
	*) echo "ERROR: unsupported handoff mode '${mode}'." >&2; return 1 ;;
	esac
	rc_engine release validate-dist --dist-dir "${directory}/dist" --digest-file "${directory}/digest" \
		--manifest-root . --stage-dir dist || return
	export DIST_DIR=dist RELEASE_FILES_MANIFEST=dist/release-files.json
	unset RC_EVIDENCE_ROOT
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

# rc_sign_release signs the hand-off its policy names: the build's, or with
# release images the publishing job's, which alone may carry a ledger.
rc_sign_release() {
	local images=${1:?rc_sign_release needs the policy release-images value}
	rc_signing_preflight
	rc_release_checkout
	case "${images}" in
	true)
		rc_read_handoff "${CI_PROJECT_DIR}/_release-images"
		rc_require_regular_file "${DIST_DIR}/release-images.json"
		;;
	false)
		rc_read_handoff "${CI_PROJECT_DIR}/_release-build"
		rc_require_absent "${DIST_DIR}/release-images.json"
		;;
	*)
		echo "ERROR: release-images must be true or false, got '${images}'" >&2
		return 1
		;;
	esac
	rc_engine release files validate --unsigned --manifest "${RELEASE_FILES_MANIFEST}" --dist-dir "${DIST_DIR}"
	# Only this trusted job may create the publication ledger. A producer must
	# not bypass import by pre-seeding that reserved path without a source ledger.
	rc_require_absent "${DIST_DIR}/release-images-publication.json"
	rc_require_absent "${DIST_DIR}/base-images-publication.json"
	if [ -f "${DIST_DIR}/release-images.json" ]; then
		rc_publication_scope
		REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
			rc_engine container release-images import --dist-dir "${DIST_DIR}" --ledger "${DIST_DIR}/release-images.json" \
			--tag "${RELEASE_TAG}" --registry "${RC_REGISTRY}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
			--expected-image-repository "${RC_REGISTRY_IMAGE}" --destination-repository "${RC_PUBLICATION_IMAGE_REPOSITORY:?}" \
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
		local base_repository="${RC_REGISTRY_IMAGE}-base"
		if [ "${RC_BASE_IMAGES}" = true ]; then base_repository="${RC_PUBLICATION_IMAGE_REPOSITORY}/bases"; fi
		REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
			rc_engine container release-images sign --ledger "${DIST_DIR}/release-images-publication.json" --method kms --key env://COSIGN_KEY \
			--server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" --registry "${RC_REGISTRY}" \
			--expected-image-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}" --expected-base-repository "${base_repository}"
	fi
	rc_engine release sign --method kms --key env://COSIGN_KEY --no-checksums-file --no-release-artifacts-dir \
		--manifest "${RELEASE_FILES_MANIFEST}" --dist-dir "${DIST_DIR}" --manifest-section evidence
	unset COSIGN_KEY COSIGN_PASSWORD
	rc_engine release files manifest --manifest "${RELEASE_FILES_MANIFEST}" --dist-dir "${DIST_DIR}"
	local handoff="${CI_PROJECT_DIR}/_release-sign"
	rc_new_handoff "${handoff}" || return
	cp -R -- "${DIST_DIR}" "${handoff}/dist"
	rc_engine release dist-digest --dist-dir "${handoff}/dist" --manifest-root . >"${handoff}/digest"
}

rc_promote_release() {
	rc_release_checkout
	rc_read_handoff "${CI_PROJECT_DIR}/_release-sign"
	rc_promote_bases
	rc_new_handoff "${CI_PROJECT_DIR}/_release-promote" || return
	if [ ! -f "${DIST_DIR}/release-images-publication.json" ]; then return; fi
	REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
		rc_engine container release-images promote --ledger "${DIST_DIR}/release-images-publication.json" \
		--server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" --registry "${RC_REGISTRY}" \
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
		--dist-dir "${DIST_DIR}" --source-repository "${RC_REGISTRY_IMAGE}/bases" \
		--expected-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}/bases" \
		--registry "${RC_REGISTRY}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
		--output-ledger "${DIST_DIR}/base-images-publication.json"
	REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
		rc_engine container base-images sign --ledger "${DIST_DIR}/base-images-publication.json" \
		--expected-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}/bases" \
		--registry "${RC_REGISTRY}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
		--workflow gitlab/release.yml --source-sha "${RELEASE_SHA}" --started-on "$(git log -1 --format=%cI)" \
		--method kms --key env://COSIGN_KEY
}

# rc_publication_scope returns explicitly, not through errexit, which a
# conditional caller disables.
rc_publication_scope() {
	if [ "${RC_PUBLICATION_PROJECT:?}" = "${CI_PROJECT_PATH:?}" ]; then
		echo 'ERROR: publication-project must be a separate project, not this one.' >&2
		return 1
	fi
	case "${RC_PUBLICATION_IMAGE_REPOSITORY:?}" in
	"${RC_REGISTRY:?}/${RC_PUBLICATION_PROJECT}" | "${RC_REGISTRY}/${RC_PUBLICATION_PROJECT}/"*) ;;
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
	local ledger
	ledger=$(realpath -e -- "${DIST_DIR}/base-images-publication.json") || return
	(
		cd -- "${RC_WORK}" || exit
		REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
			rc_engine container base-images promote --ledger "${ledger}" \
			--expected-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}/bases" \
			--registry "${RC_REGISTRY}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
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
		--server-url "${RC_SERVER_URL}" --repository "${RC_PUBLICATION_PROJECT}" --registry "${RC_REGISTRY}" \
		--expected-image-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}"
}

# rc_release_summary writes the release overview. Its job runs only when every
# scheduled release job succeeded, so each included row is a success by construction.
rc_release_summary() {
	local images=${1:-false} arm64_tags=${2:-[]} name jobs=()
	for name in "Check: signing access" "Check: publishing access" "Check: registry access" \
		"Check: release request" "Check: consumer checks" "Prepare: changelog" "Prepare: version" \
		"Prepare: release commit and tag" "Build: release files" "Images: plan" "Images: build amd64" \
		"Images: build arm64" "Images: publish" "Sign: files and images" \
		"Publish: images" "Publish: release" "Publish: tidy up"; do
		if [ "${name}" = "Prepare: version" ] && [ -z "${RC_VERSION_BUMP:-}" ]; then continue; fi
		if [ "${name}" = "Check: registry access" ] && [ "${images}" != true ] && [ "${RC_BASE_IMAGES:-false}" != true ]; then continue; fi
		case "${name}" in "Images: "*) if [ "${images}" != true ]; then continue; fi ;; esac
		if [ "${name}" = "Images: build arm64" ] && [ "${arm64_tags}" = '[]' ]; then continue; fi
		jobs+=(--job "${name}=success")
	done
	rc_engine report release --release-version "${RELEASE_TAG:?}" --release-branch "${RC_RELEASE_BRANCH:?}" \
		--release-actor "${GITLAB_USER_LOGIN:-}" "${jobs[@]}"
}
