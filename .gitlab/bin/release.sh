#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Trusted GitLab orchestration. The project must select a pinned external CI
# policy. Only "Check: consumer checks", "Prepare: changelog", the "Prepare:
# version <family>" and "Build: <family>" jobs and "Build: release files"
# execute consumer code, and the "Images: build" jobs the consumer's
# Containerfiles, with the job token alone. "Publish: npm packages" holds
# the job token alone too, and runs no consumer code. Of a base stage,
# "Base: plan" runs the project's planner and "Base: build" its
# Containerfiles, with no release secret; the stage's other jobs run no
# consumer code. Native artifacts carry data; they are never dotenv reports
# or sourced.
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
    RELEASE_PACKAGE_TOKEN \
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
      promote:RELEASE_REGISTRY_TOKEN | cleanup:RELEASE_REGISTRY_TOKEN | \
      packages:RELEASE_PACKAGE_TOKEN | \
      base-parent:RELEASE_REGISTRY_TOKEN | base-publish:RELEASE_REGISTRY_TOKEN | \
      base-promote:RELEASE_REGISTRY_TOKEN | base-promote:RELEASE_COSIGN_PRIVATE_KEY | \
      base-promote:RELEASE_COSIGN_PASSPHRASE | base-promote:RELEASE_GPG_PRIVATE_KEY | \
      base-promote:RELEASE_GPG_PASSPHRASE) allowed=true ;;
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

# rc_release_plan plans the checkout's release from the artifacts-config
# declaration, as "Release: plan" did at the request: the arguments name what
# to write. Without a declaration it plans no bump and no family. The base
# stages are the policy's, so the planned jobs name theirs as well.
rc_release_plan() {
  local -a file=()
  if [ -n "${RC_ARTIFACTS_CONFIG?}" ]; then file=(--file "${RC_ARTIFACTS_CONFIG}"); fi
  rc_engine plan gitlab-release-pipeline "${file[@]}" \
    --base-stages "${RC_BASE_STAGES?}" --image-builder "${RC_IMAGE_BUILDER?}" "$@"
}

rc_release_checkout() {
  rc_checkout "refs/tags/${RELEASE_TAG}"
  export RELEASE_SHA
  RELEASE_SHA=$(git rev-parse HEAD)
  # The release commit is the tested request plus the changelog, and the
  # plan's version bump: each planned project's changelog copy and version
  # files, native ones a verified version substitution. Nothing else is built.
  rc_release_plan --prepare-plan-output "${RC_WORK:?}/prepare-plan.json"
  rc_engine release validate-tag --release-sha "${RELEASE_SHA}" --tag "${RELEASE_TAG}" \
    --expected-parent "${RELEASE_REQUEST_SHA}" --changelog-only --prepare-plan-file "${RC_WORK}/prepare-plan.json" \
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

# rc_bump_version runs the native version tool of one family's planned
# projects at the request, in the family's runtime image, and hands over only
# the patch of their native version files and its digest. The job holds no
# credential. The plan names the project type the family's native tool bumps.
rc_bump_version() {
  local family=$1 kind
  rc_request_checkout
  rc_release_plan --prepare-plan-output "${RC_WORK:?}/prepare-plan.json" \
    --family "${family}" --project-type-output "${RC_WORK}/project-type"
  kind=$(cat -- "${RC_WORK}/project-type")
  # The plan copies the full changelog into each project; take the rendered one.
  rc_require_regular_file "${CI_PROJECT_DIR}/_release-render/CHANGELOG.md"
  cp --remove-destination -- "${CI_PROJECT_DIR}/_release-render/CHANGELOG.md" CHANGELOG.md
  # The bump's own outputs, apart from the job's: its file-pattern names the
  # version files of this family's planned projects alone.
  CI_OUTPUT="${RC_WORK}/bump-plan.env" rc_engine version bump-plan --plan-json "$(cat -- "${RC_WORK}/prepare-plan.json")" \
    --version "${RELEASE_VERSION:?}" --release-policy "${RELEASE_POLICY:?}" --project-type "${kind}" \
    --maven-cli-opts '--batch-mode --no-transfer-progress --errors --fail-at-end'
  local line pathspec
  local -a pathspecs=()
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in FILE_PATTERN=*) read -r -a pathspecs <<<"${line#FILE_PATTERN=}" ;; esac
  done <"${RC_WORK}/bump-plan.env"
  local -a native=()
  for pathspec in "${pathspecs[@]}"; do
    # The release commit's job writes the changelog copies itself.
    case "${pathspec##*/}" in CHANGELOG.md) ;; *) native+=("${pathspec}") ;; esac
  done
  local handoff="${CI_PROJECT_DIR}/_release-bump-${family}"
  rc_new_handoff "${handoff}" || return
  # Literal paths and plain text whatever the project's Git configuration
  # says, and this family's native version files alone. A family without a
  # planned project hands on an empty patch.
  if [ "${#native[@]}" -eq 0 ]; then
    : >"${handoff}/version-bump.patch"
  else
    git -c core.quotePath=true diff --binary --no-ext-diff --no-textconv --no-color --no-renames \
      --src-prefix=a/ --dst-prefix=b/ -- "${native[@]}" >"${handoff}/version-bump.patch"
  fi
  sha256sum -- "${handoff}/version-bump.patch" | cut -d' ' -f1 >"${handoff}/digest"
}

rc_prepare_release() {
  rc_request_checkout
  local file family bump patch="${RC_WORK:?}/version-bump.patch"
  local -a include=()
  git -c core.hooksPath=/dev/null checkout -b "${RC_RELEASE_BRANCH}"
  for file in CHANGELOG.md commit-msg.txt; do
    rc_require_regular_file "${CI_PROJECT_DIR}/_release-render/${file}"
    cp --remove-destination -- "${CI_PROJECT_DIR}/_release-render/${file}" "${file}"
  done
  # Text checks only, no build tool: each family's patch matches its digest,
  # and the engine admits the joined patch only as version substitutions of
  # the plan's project files, applies it, and writes the changelog copies and
  # the other types' version files itself.
  : >"${patch}"
  for family in java node rust; do
    bump="${CI_PROJECT_DIR}/_release-bump-${family}"
    if [ ! -e "${bump}" ] && [ ! -L "${bump}" ]; then continue; fi
    for file in version-bump.patch digest; do
      rc_require_regular_file "${bump}/${file}" || return
    done
    if [ "$(sha256sum -- "${bump}/version-bump.patch" | cut -d' ' -f1)" != "$(head -c 64 -- "${bump}/digest")" ]; then
      echo "ERROR: the ${family} version patch differs from its job's digest." >&2
      return 1
    fi
    cat -- "${bump}/version-bump.patch" >>"${patch}"
  done
  rc_release_plan --prepare-plan-output "${RC_WORK}/prepare-plan.json"
  # The engine names the paths it wrote besides the root changelog, and the
  # release commit carries those alone.
  rc_engine version bump-plan --plan-json "$(cat -- "${RC_WORK}/prepare-plan.json")" --version "${RELEASE_VERSION:?}" \
    --release-policy "${RELEASE_POLICY:?}" --patch "${patch}" --patch-sha256 "$(sha256sum -- "${patch}" | cut -d' ' -f1)" \
    --paths-file "${RC_WORK}/bump-paths"
  if [ -s "${RC_WORK}/bump-paths" ]; then
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

# rc_family_checkout checks the release commit out for one family's build
# job and names, for the build commands, the family and its planned
# artifacts.
rc_family_checkout() {
  local family=$1
  rc_release_checkout || return
  rc_release_plan --family "${family}" --artifacts-output "${RC_WORK:?}/family-artifacts" || return
  export FORGE_CI_BUILD_FAMILY="${family}" FORGE_CI_ARTIFACTS
  FORGE_CI_ARTIFACTS=$(tr '\n' ' ' <"${RC_WORK}/family-artifacts")
  FORGE_CI_ARTIFACTS=${FORGE_CI_ARTIFACTS% }
  echo "build family ${family}: ${FORGE_CI_ARTIFACTS}"
}

# rc_family_handoff hands one family's dist/ and image-context/ to the release
# files job, without a manifest: that job declares the files once.
rc_family_handoff() {
  local handoff="${CI_PROJECT_DIR}/_release-build-$1" tree
  if [ -e dist/release-files.json ] || [ -L dist/release-files.json ]; then
    echo 'ERROR: a family build leaves its files in dist/; the build commands declare them in "Build: release files", where FORGE_CI_BUILD_FAMILY is empty.' >&2
    return 1
  fi
  rc_new_handoff "${handoff}" || return
  for tree in dist image-context; do
    rc_engine release gather-exports --allow-missing --source "${tree}" --path "${handoff}/${tree}" || return
  done
}

# rc_planned_jobs writes the names of the release child's plan-driven jobs,
# one a line, to $1, planned again from the checkout as "Release: plan"
# planned them.
rc_planned_jobs() {
  rc_release_plan --job-names-output "$1"
}

# rc_gather_families lays every family build's dist/ and image-context/ into
# the release files job's, refusing a path two of them, or the checkout,
# both hold. The hand-offs must be exactly the planned build jobs', so a
# child that diverged from the plan cannot skip a build. The build commands
# then run with no family.
rc_gather_families() {
  local tree family planned handoff
  local -a sources
  rc_planned_jobs "${RC_WORK:?}/planned-jobs" || return
  for family in java node go rust android; do
    planned=false handoff="${CI_PROJECT_DIR}/_release-build-${family}"
    if grep -qxF "Build: ${family}" "${RC_WORK}/planned-jobs"; then planned=true; fi
    if [ "${planned}" = true ] && { [ ! -d "${handoff}" ] || [ -L "${handoff}" ]; }; then
      echo "ERROR: the plan builds the ${family} family, but \"Build: ${family}\" handed nothing on." >&2
      return 1
    fi
    if [ "${planned}" = false ] && { [ -e "${handoff}" ] || [ -L "${handoff}" ]; }; then
      echo "ERROR: \"Build: ${family}\" handed files on, but the plan builds no ${family} family." >&2
      return 1
    fi
  done
  for tree in dist image-context; do
    sources=()
    for family in java node go rust android; do
      sources+=(--source "${CI_PROJECT_DIR}/_release-build-${family}/${tree}")
    done
    rc_engine release gather-exports --allow-missing --path "${tree}" "${sources[@]}" || return
  done
  export FORGE_CI_BUILD_FAMILY='' FORGE_CI_ARTIFACTS=''
}

# rc_planned_packages writes the directories of the npm projects the
# checkout's plan sends to forge-packages, one a line, to $1.
rc_planned_packages() {
  rc_release_plan --packages-output "$1"
}

# rc_packages fills rc_package_files with the packed npm packages of the
# dist at $1 and holds them to the plan and the checkout: the engine names,
# for each npm project the plan sends to forge-packages, the *.tgz at the
# root of dist that packs the name and version of the project's own
# package.json, and refuses a project without one and a *.tgz of no project.
# A release that sends none may carry any file.
rc_packages() {
  local dist=$1 names directory name
  local -a projects=()
  rc_planned_packages "${RC_WORK:?}/planned-packages" || return
  rc_require_regular_file "${RC_WORK}/planned-packages" || return
  rc_package_files=()
  while IFS= read -r directory; do
    projects+=(--project-dir "${directory}")
  done <"${RC_WORK}/planned-packages"
  if [ "${#projects[@]}" -eq 0 ]; then return 0; fi
  names=$(rc_engine publish forge-packages npm-tarballs --dist-dir "${dist}" "${projects[@]}") || return
  while IFS= read -r name; do
    rc_package_files+=("${dist}/${name}")
  done <<<"${names}"
}

# rc_publish_packages publishes the signed release's packed npm packages to
# this project's package registry, each under the release's own tag, which
# the engine holds to the packed version. The credential is the release's
# package token: the job token of a hardened project writes no package, and
# every job that runs the project's code holds one. The checkout is the
# release commit's, so each package is its project's there. A package an
# earlier run of this job published is left as it is when its content is
# this tarball's. The packages are data: npm runs no script of theirs.
rc_publish_packages() {
  local file stage
  : "${RELEASE_PACKAGE_TOKEN:?}"
  rc_release_checkout || return
  rc_read_handoff "${CI_PROJECT_DIR}/_release-sign" || return
  rc_packages "${DIST_DIR}" || return
  if [ "${#rc_package_files[@]}" -eq 0 ]; then
    echo 'ERROR: "Publish: npm packages" ran, but the plan sends no npm package to forge-packages.' >&2
    return 1
  fi
  for file in "${rc_package_files[@]}"; do
    stage=$(mktemp -d "${RC_WORK}/package.XXXXXX") || return
    cp -- "${file}" "${stage}/" || return
    rc_engine publish forge-packages deploy --project-type npm --working-dir "${stage}" \
      --npm-tag "release-${RELEASE_VERSION:?}" || return
  done
}

rc_build_handoff() {
  # Base images are built, signed and promoted by the base stage alone.
  if [ -e dist/base-images.json ] || [ -L dist/base-images.json ]; then
    echo 'ERROR: dist/base-images.json is not a release file: name the base images in the release policy as base-stages, with base-plan, and the base stage builds, signs and promotes them.' >&2
    return 1
  fi
  # Release images are built and recorded by the image jobs alone; a build
  # that pushed its own has nothing the signer may import.
  if [ -e dist/release-images.json ] || [ -L dist/release-images.json ]; then
    echo 'ERROR: dist/release-images.json is written by the image jobs; declare the images under containers: in artifacts.yml, set release-images: true, and leave their build context in image-context/.' >&2
    return 1
  fi
  # Before anything is signed: the package job would refuse the same dist
  # only after the release is published.
  rc_packages dist || return
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
  rc_plan_release_images "$1" "$2" "${CI_PROJECT_DIR}/_release-images-plan/plan.json"
}

# rc_plan_release_images writes the image jobs' plan of the checkout to
# output, refusing what no image job would carry out.
rc_plan_release_images() {
  local config=$1 platforms=$2 output=$3 label labels=()
  # The labels input as written, one key=value a line; the engine admits
  # each and refuses one that replaces a standard provenance label.
  while IFS= read -r label; do
    if [ -n "${label}" ]; then labels+=(--label "${label}"); fi
  done <<<"${RC_IMAGE_LABELS?}"
  rc_engine container release-images plan --file "${config}" --image-plan-json '' \
    --release-tag "${RELEASE_TAG}" --release-platforms "${platforms}" "${labels[@]}" \
    --image-repository "${RC_REGISTRY_IMAGE:?}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
    --registry-username gitlab-ci-token \
    --runner-input "runner-tags=${RC_RUNNER_TAGS?}" \
    --runner-input "signer-runner-tags=${RC_SIGNER_RUNNER_TAGS?}" \
    --runner-input "build-runner-tags-linux-amd64=${RC_BUILD_RUNNER_TAGS_LINUX_AMD64?}" \
    --runner-input "build-runner-tags-linux-arm64=${RC_BUILD_RUNNER_TAGS_LINUX_ARM64?}" \
    --output "${output}"
}

# rc_check_release_images refuses, at the release request and before Prepare
# pushes the release commit and tag, a policy whose image stage would stop
# after it: a builder without its job image, a publication project that is
# not separate, or a plan no image job carries out. The arguments are the
# policy's literal inputs. The image jobs plan again at the release commit,
# which differs from the request only by the changelog and version bump.
rc_check_release_images() {
  local images=$1 builder=$2 job_image=$3 config=$4 platforms=$5
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
  rc_plan_release_images "${config}" "${platforms}" "${RC_WORK:?}/release-images-plan.json"
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
  command -v syft >/dev/null || {
    echo 'ERROR: Images: publish requires Syft in job-image.' >&2
    return 1
  }
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
    # The unsigned transport of a base stage's publication, read by its
    # promotion alone; signed/publication handoffs retain their own contract.
    rc_engine release validate-dist --dist-dir "${directory}/payload" --digest-file "${directory}/digest" \
      --canonical-payload --stage-dir verified-payload || return
    export DIST_DIR=verified-payload/dist RELEASE_FILES_MANIFEST=verified-payload/dist/release-files.json
    export RC_EVIDENCE_ROOT=verified-payload
    return
    ;;
  dist) ;;
  *)
    echo "ERROR: unsupported handoff mode '${mode}'." >&2
    return 1
    ;;
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

# rc_release_file_signing asks the engine how the checkout's declaration
# has its checksums and SBOMs signed, and sets rc_file_signing to the answer:
# gpg when it says sign.method gpg or sign.files-method gpg, cosign
# otherwise, also without a sign block or a declaration. Any other answer stops the job.
rc_release_file_signing() {
  local outputs="${RC_WORK:?}/release-file-signing.env"
  local -a file=()
  if [ -n "${RC_ARTIFACTS_CONFIG?}" ]; then file=(--file "${RC_ARTIFACTS_CONFIG}"); fi
  : >"${outputs}"
  CI_OUTPUT="${outputs}" rc_engine plan release-file-signing "${file[@]}" || return
  rc_file_signing=$(rc_output "${outputs}" RELEASE_FILE_SIGNING) || return
  case "${rc_file_signing}" in
  gpg | cosign) ;;
  *)
    echo "ERROR: the engine did not say gpg or cosign for the release file signing, but '${rc_file_signing}'." >&2
    return 1
    ;;
  esac
}

# rc_signing_preflight checks the signer's tools and keys. Its arguments say
# whether the job signs distro packages, and how it signs the checksums and
# SBOMs, gpg or cosign: either of distro packages and gpg needs the GPG key.
rc_signing_preflight() {
  local distro_packages=${1:?rc_signing_preflight needs the sign-distro-packages value}
  local files=${2:?rc_signing_preflight needs the release file signing method}
  case "${files}" in
  gpg | cosign) ;;
  *)
    echo "ERROR: the release files are signed with gpg or cosign, not '${files}'." >&2
    return 1
    ;;
  esac
  SIGNER_STRICT=1 bash "${RC_HELPERS_DIR}/.forgejo/scripts/verify-signer-toolchain.sh"
  rc_public_key
  export COSIGN_KEY="${RELEASE_COSIGN_PRIVATE_KEY:?}" COSIGN_PASSWORD="${RELEASE_COSIGN_PASSPHRASE:?}"
  rc_engine validate signing-key --key env://COSIGN_KEY --trusted-public-key "${RC_WORK}/trusted.pub" \
    --trusted-public-key-digest "${RC_WORK}/cosign.pub.sha256"
  local gpg_required=''
  if [ "${distro_packages}" = true ]; then
    gpg_required='is required while sign-distro-packages is true; set it, or set sign-distro-packages: false'
  elif [ "${files}" = gpg ]; then
    gpg_required='is required while artifacts.yml says sign.method or sign.files-method gpg: it signs the checksums and SBOMs'
  fi
  if [ -n "${gpg_required}" ]; then
    : "${RELEASE_GPG_PRIVATE_KEY:?${gpg_required}}" "${RELEASE_GPG_PASSPHRASE:?${gpg_required}}" \
      "${RELEASE_GPG_FINGERPRINT:?${gpg_required}}"
  fi
  unset COSIGN_KEY COSIGN_PASSWORD
}

# rc_check_signing_access is "Check: signing access": the signing preflight
# for the release request's declaration, before the release commit exists.
# The request is checked out as data; nothing of it runs.
rc_check_signing_access() {
  rc_request_checkout
  rc_release_file_signing
  rc_signing_preflight "${RC_SIGN_DISTRO_PACKAGES}" "${rc_file_signing}"
}

# rc_sign_release_files signs the checksums and SBOMs the way $1 says. gpg
# signs them with the GPG key, which the engine refuses unless its
# fingerprint is RELEASE_GPG_FINGERPRINT and refuses to load while the runner
# has swap on, and writes an .asc beside each, dated at the release commit
# so the signatures are reproducible. The engine then holds the GPG key, its
# passphrase and its fingerprint, and no other signing or publishing secret
# of the job, as the Forgejo step does. cosign signs them with the Cosign key.
rc_sign_release_files() {
  local epoch
  case "${1}" in
  gpg)
    epoch=$(git log -1 --format=%ct "${RELEASE_SHA:?}") || return
    (
      key="${RELEASE_GPG_PRIVATE_KEY:?}" passphrase="${RELEASE_GPG_PASSPHRASE:?}"
      fingerprint="${RELEASE_GPG_FINGERPRINT:?}"
      unset RELEASE_SSH_SIGNING_KEY RELEASE_COSIGN_PRIVATE_KEY RELEASE_COSIGN_PASSPHRASE \
        RELEASE_GPG_PRIVATE_KEY RELEASE_GPG_PASSPHRASE RELEASE_TOKEN RELEASE_REGISTRY_USER \
        RELEASE_REGISTRY_TOKEN RELEASE_PACKAGE_TOKEN SSH_SIGNING_KEY COSIGN_KEY COSIGN_PASSWORD \
        GPG_SIGNING_KEY GPG_SIGNING_PASSWORD GITLAB_TOKEN REGISTRY_USER REGISTRY_TOKEN \
        CI_JOB_TOKEN CI_REGISTRY_PASSWORD CI_DEPENDENCY_PROXY_PASSWORD CI_DEPLOY_PASSWORD
      GPG_PRIVATE_KEY="${key}" GPG_PASSPHRASE="${passphrase}" SOURCE_DATE_EPOCH="${epoch}" \
        rc_engine release sign --method gpg --fingerprint "${fingerprint}" --checksums-from-manifest \
        --manifest "${RELEASE_FILES_MANIFEST:?}" --dist-dir "${DIST_DIR:?}" --manifest-section sboms --no-release-artifacts-dir
    )
    ;;
  cosign)
    COSIGN_KEY="${RELEASE_COSIGN_PRIVATE_KEY:?}" COSIGN_PASSWORD="${RELEASE_COSIGN_PASSPHRASE:?}" \
      rc_engine release sign --method kms --key env://COSIGN_KEY --checksums-from-manifest \
      --manifest "${RELEASE_FILES_MANIFEST:?}" --dist-dir "${DIST_DIR:?}" --manifest-section sboms --no-release-artifacts-dir
    ;;
  *)
    echo "ERROR: the release files are signed with gpg or cosign, not '${1}'." >&2
    return 1
    ;;
  esac
}

# rc_sign_release signs the hand-off its policy names: the build's, or with
# release images the publishing job's, which alone may carry a ledger.
rc_sign_release() {
  local images=${1:?rc_sign_release needs the policy release-images value}
  rc_release_checkout
  rc_release_file_signing
  rc_signing_preflight "${RC_SIGN_DISTRO_PACKAGES}" "${rc_file_signing}"
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
  if [ -f "${DIST_DIR}/release-images.json" ]; then
    rc_publication_scope
    REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
      rc_engine container release-images import --dist-dir "${DIST_DIR}" --ledger "${DIST_DIR}/release-images.json" \
      --tag "${RELEASE_TAG}" --registry "${RC_REGISTRY}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
      --expected-image-repository "${RC_REGISTRY_IMAGE}" --destination-repository "${RC_PUBLICATION_IMAGE_REPOSITORY:?}" \
      --output-ledger "${DIST_DIR}/release-images-publication.json"
  fi
  if [ "${RC_SIGN_DISTRO_PACKAGES}" = true ]; then
    GPG_SIGNING_KEY="${RELEASE_GPG_PRIVATE_KEY:?}" GPG_SIGNING_PASSWORD="${RELEASE_GPG_PASSPHRASE:?}" \
      rc_engine release gpg sign-packages --fingerprint "${RELEASE_GPG_FINGERPRINT:?}" --dir "${DIST_DIR}"
  fi
  rc_sign_release_files "${rc_file_signing}"
  # Provenance, evidence and images are signed with the Cosign key either way.
  export COSIGN_KEY="${RELEASE_COSIGN_PRIVATE_KEY:?}" COSIGN_PASSWORD="${RELEASE_COSIGN_PASSPHRASE:?}"
  rc_engine release provenance --checksums-from-manifest --manifest "${RELEASE_FILES_MANIFEST}" \
    --dist-dir "${DIST_DIR}" --go-sum go.sum --workflow gitlab/release.yml \
    --ref-name "${RELEASE_TAG}" --source-sha "${RELEASE_SHA}" --started-on-commit "${RELEASE_SHA}" \
    --output "${DIST_DIR}/slsa-provenance.intoto.json" --method kms --key env://COSIGN_KEY \
    --bundle "${DIST_DIR}/slsa-provenance.intoto.json.bundle"
  if [ -f "${DIST_DIR}/release-images-publication.json" ]; then
    local base_repository="${RC_REGISTRY_IMAGE}-base"
    if [ -n "${RC_BASE_STAGES?}" ]; then base_repository="${RC_PUBLICATION_IMAGE_REPOSITORY}/bases"; fi
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
  rc_new_handoff "${CI_PROJECT_DIR}/_release-promote" || return
  if [ ! -f "${DIST_DIR}/release-images-publication.json" ]; then return; fi
  REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
    rc_engine container release-images promote --ledger "${DIST_DIR}/release-images-publication.json" \
    --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" --registry "${RC_REGISTRY}" \
    --expected-image-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}" --journal "${CI_PROJECT_DIR}/_release-promote/image-promotions.jsonl"
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

# The base stage: the policy's base-stages, a root and then the children of
# a promoted parent, one stage at a time, as Forgejo's base-images.yml and
# the signer workflows run one. "Base: plan" runs the project's planner and
# "Base: build" its Containerfiles; neither holds a release secret. "Base:
# parent", "Base: publish" and "Base: sign and promote" run nothing of the
# project's. The candidates go to the publication project's bases repository
# with the release's registry credential: the job token writes no image
# there, since every job that runs the project's code holds one.

# rc_output prints the value an engine call wrote under the name $2 to its
# outputs file $1; a name it did not write is a failure.
rc_output() {
  local line value='' found=false
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in "$2="*) value=${line#"$2="} found=true ;; esac
  done <"$1"
  if [ "${found}" = false ]; then
    echo "ERROR: the engine wrote no $2 output." >&2
    return 1
  fi
  printf '%s\n' "${value}"
}

# rc_base_flavors prints every flavor of the policy's base-stages, one a line.
rc_base_flavors() {
  local spec
  local -a specs
  read -r -a specs <<<"${RC_BASE_STAGES?}"
  for spec in "${specs[@]}"; do
    spec=${spec%%:*}
    printf '%s\n' "${spec//+/$'\n'}"
  done
}

# rc_base_stage holds a generated job's stage and parent to the policy's
# base-stages, so a child pipeline that diverged from the plan builds no
# other graph, and names the one repository the bases live in.
rc_base_stage() {
  local stage=$1 parent=$2 spec found=false
  local -a specs
  read -r -a specs <<<"${RC_BASE_STAGES?}"
  for spec in "${specs[@]}"; do
    if [ "${spec}" = "${stage}${parent:+:${parent}}" ]; then found=true; fi
  done
  if [ "${found}" = false ]; then
    echo "ERROR: base-stages names no stage '${stage}${parent:+:${parent}}'." >&2
    return 1
  fi
  rc_publication_scope || return
  rc_base_repository="${RC_PUBLICATION_IMAGE_REPOSITORY}/bases"
}

# rc_base_stage_args are what every verb of the stage $1 takes, at the
# release commit. The plan is the project's: each verb holds it to exactly
# the flavors the policy names for the stage. Bases are content-addressed:
# the plan names their tags, so the release tag the image verbs require
# names no part of them.
rc_base_stage_args() {
  rc_base_args=(--plan "${CI_PROJECT_DIR}/_release-base-plan/plan.json" --stage-flavors "${1//+/,}"
    --image-repository "${rc_base_repository:?}"
    --release-tag v0.0.0 --release-sha "${RELEASE_SHA:?}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}")
}

# rc_base_projection reads a {ref,input_id} parent projection a signer job
# wrote, from $1, into rc_parent_json, rc_parent_ref and rc_parent_id. The
# ref is a digest in the bases repository, or the file is refused.
rc_base_projection() {
  local pattern='^\{"ref":"([^"@[:space:]]+)@(sha256:[0-9a-f]{64})","input_id":"([0-9a-f]{64})"\}$'
  rc_require_regular_file "$1" || return
  rc_parent_json=$(head -c 1024 -- "$1") || return
  if [[ ! "${rc_parent_json}" =~ ${pattern} ]] || [ "${BASH_REMATCH[1]}" != "${rc_base_repository:?}" ]; then
    echo "ERROR: $1 is no verified parent projection of ${rc_base_repository}." >&2
    return 1
  fi
  rc_parent_ref="${BASH_REMATCH[1]}@${BASH_REMATCH[2]}" rc_parent_id=${BASH_REMATCH[3]}
}

rc_base_login() {
  # Never the job token: without the release's credential the job stops.
  if [ -z "${RELEASE_REGISTRY_TOKEN:-}" ] || [ -z "${RELEASE_REGISTRY_USER:-}" ]; then
    echo 'ERROR: the base stage needs RELEASE_REGISTRY_TOKEN and RELEASE_REGISTRY_USER of the release/registry environments.' >&2
    return 1
  fi
  printf '%s' "${RELEASE_REGISTRY_TOKEN:?}" | rc_engine container login --registry "${rc_base_repository:?}" \
    --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
    --registry-username "${RELEASE_REGISTRY_USER:?}" --registry-password-file - --auth-file "$1"
}

# rc_base_plan runs the policy's base-plan command at the release commit, as
#   <base-plan> <flavor>[,<flavor>...] <repository> [<parent-ref> <parent-input-id>]
# and admits what it prints as one stage: a root, or children that each
# build on the promoted parent and on nothing else. The parent is the one
# its promotion verified.
rc_base_plan() {
  local stage=$1 parent=$2 handoff="${CI_PROJECT_DIR}/_release-base-plan"
  local -a command parent_args=()
  rc_base_stage "${stage}" "${parent}" || return
  read -r -a command <<<"${RC_BASE_PLAN?}"
  if [ "${#command[@]}" -eq 0 ]; then
    echo 'ERROR: base-stages needs base-plan, the command that prints the plan of one stage.' >&2
    return 1
  fi
  if [ -n "${parent}" ]; then
    rc_base_projection "${CI_PROJECT_DIR}/_release-base-promote/${parent}.json" || return
    parent_args=("${rc_parent_ref}" "${rc_parent_id}")
  fi
  rc_release_checkout || return
  rc_new_handoff "${handoff}" || return
  # The planner's environment is without the job token. That is no boundary:
  # it runs in this container and can read the token from the job shell's
  # /proc environment, the trust Check: consumer checks and Build: release
  # files already give the project's code. The job holds no release secret,
  # and the bases repository takes no job token.
  (
    unset CI_JOB_TOKEN CI_REGISTRY_PASSWORD CI_DEPLOY_PASSWORD CI_DEPENDENCY_PROXY_PASSWORD
    "${command[@]}" "${stage//+/,}" "${rc_base_repository}" "${parent_args[@]}"
  ) >"${handoff}/plan.json" || return
  rc_base_stage_args "${stage}"
  # The stage builds for amd64, natively, on build-runner-tags-linux-amd64:
  # the one base build job. No arm64 input is passed, so an arm64 base is
  # refused whatever build-runner-tags-linux-arm64 names.
  local -a admitted=(--output "${RC_WORK:?}/admitted-base-plan.json"
    --runner-input "build-runner-tags-linux-amd64=${RC_BUILD_RUNNER_TAGS_LINUX_AMD64?}")
  if [ -n "${parent}" ]; then
    rc_engine container base-images child-base plan --parent-base-json "${rc_parent_json}" \
      --image-builder "${RC_IMAGE_BUILDER?}" "${rc_base_args[@]}" "${admitted[@]}"
  else
    rc_engine container base-images root-base plan "${rc_base_args[@]}" "${admitted[@]}"
  fi
}

# rc_base_parent authenticates the promoted parent again, by its digest and
# against the pinned key, and fetches it for every build of the stage with
# the release's registry credential. It runs nothing of the project's; the
# build starts from these bytes and from this job's copy of the projection.
rc_base_parent() {
  local stage=$1 parent=$2 handoff="${CI_PROJECT_DIR}/_release-base-parent" auth="${RC_WORK:?}/registry-auth.json" verified
  rc_base_stage "${stage}" "${parent}" || return
  rc_base_projection "${CI_PROJECT_DIR}/_release-base-promote/${parent}.json" || return
  rc_public_key || return
  rc_release_checkout || return
  rc_base_stage_args "${stage}"
  cd -- "${RC_WORK}" || return
  rc_base_login "${auth}" || return
  verified=$(rc_engine container base-images authenticate-parent --auth-file "${auth}" \
    --expected-ref "${rc_parent_ref}" --base-inputs-json "[{\"flavor\":\"${parent}\",\"base_input_id\":\"${rc_parent_id}\"}]" \
    --expected-repository "${rc_base_repository}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
    --expected-source "${RC_SERVER_URL}/${CI_PROJECT_PATH}" --expected-workflow gitlab/release.yml \
    --cosign-public-key-path trusted.pub \
    --cosign-public-key-sha256 "${RC_PUBLIC_KEY_SHA256:?}") || return
  if [ "${verified}" != "${rc_parent_json}" ]; then
    echo "ERROR: the parent ${parent} verified as another projection than its promotion handed on." >&2
    return 1
  fi
  rc_engine container base-images child-base fetch "${rc_base_args[@]}" --parent-base-json "${rc_parent_json}" \
    --inputs-dir registry-inputs --auth-file "${auth}" || return
  rm -f -- "${auth}"
  rc_new_handoff "${handoff}" || return
  cp -R -- registry-inputs "${handoff}/registry-inputs" || return
  printf '%s\n' "${rc_parent_json}" >"${handoff}/parent-base.json"
}

# rc_base_build builds every image of the stage to an OCI layout, natively,
# with no registry write and no release secret. The builder's environment is
# without the job token, which a RUN step can still read from the job
# shell's /proc environment, as in the image build jobs. A child builds from
# the parent job's bytes, on the projection that job wrote.
rc_base_build() {
  local stage=$1 parent=$2 handoff="${CI_PROJECT_DIR}/_release-base-build" created
  rc_base_stage "${stage}" "${parent}" || return
  export CI_IMAGE_BUILDER="${RC_IMAGE_BUILDER?}" CI_IMAGE_BUILDER_JOB_IMAGE="${RC_BUILDER_JOB_IMAGE?}"
  rc_image_builder || return
  rc_release_checkout || return
  rc_base_stage_args "${stage}"
  created=$(TZ=UTC0 git show -s --date=format-local:%Y-%m-%dT%H:%M:%SZ --format=%cd HEAD)
  if [ -n "${parent}" ]; then
    rc_base_projection "${CI_PROJECT_DIR}/_release-base-parent/parent-base.json" || return
    cp -R -- "${CI_PROJECT_DIR}/_release-base-parent/registry-inputs" "${RC_WORK}/registry-inputs" || return
  fi
  (
    cd -- "${RC_WORK}" || exit
    # As the image builds: the job token is not handed to the engine.
    unset CI_JOB_TOKEN CI_REGISTRY_PASSWORD CI_DEPLOY_PASSWORD CI_DEPENDENCY_PROXY_PASSWORD
    if [ -n "${parent}" ]; then
      rc_engine container base-images child-base build --parent-base-json "${rc_parent_json}" \
        --inputs-dir registry-inputs "${rc_base_args[@]}" --arch amd64 --created "${created}" \
        --source-dir consumer --metadata-dir staged --image-builder "${CI_IMAGE_BUILDER}"
    else
      rc_engine container base-images root-base build "${rc_base_args[@]}" --arch amd64 --created "${created}" \
        --source-dir consumer --metadata-dir staged --image-builder "${CI_IMAGE_BUILDER}"
    fi
  ) || return
  if [ "${CI_IMAGE_BUILDER}" = kaniko ]; then export PATH="/kaniko/tools:/kaniko:${PATH}"; fi
  rc_new_handoff "${handoff}" || return
  cp -R -- "${RC_WORK}/staged/." "${handoff}/" || return
  find "${handoff}" \( -type d -o -type f \) -exec chmod u=rwX,go=rX -- {} +
}

# rc_base_publish scans every layout of the stage and pushes the unsigned
# staging candidates with the release's registry credential, never the job
# token. The layouts are data, admitted against the plan before the first
# upload. It hands on the ledger and the SBOMs it pins, digested.
rc_base_publish() {
  local stage=$1 parent=$2 handoff="${CI_PROJECT_DIR}/_release-base-publish" auth="${RC_WORK:?}/registry-auth.json"
  # The pinned job image must carry Syft. Never download a scanner here.
  command -v syft >/dev/null || {
    echo 'ERROR: Base: publish requires Syft in job-image.' >&2
    return 1
  }
  rc_base_stage "${stage}" "${parent}" || return
  rc_release_checkout || return
  rc_base_stage_args "${stage}"
  if [ -n "${parent}" ]; then
    rc_base_projection "${CI_PROJECT_DIR}/_release-base-parent/parent-base.json" || return
  fi
  # shellcheck source=scripts/bootstrap/install-trivy.sh
  source "${RC_HELPERS_DIR}/scripts/bootstrap/install-trivy.sh"
  install_trivy
  rc_base_login "${auth}" || return
  # In the checkout's top level, where the gate reads .trivyignore.yaml.
  if [ -n "${parent}" ]; then
    rc_engine container base-images child-base publish --enforce-vulnerabilities="${RC_ADMIN_VULNERABILITIES:?}" \
      --parent-base-json "${rc_parent_json}" "${rc_base_args[@]}" \
      --layouts-dir "${CI_PROJECT_DIR}/_release-base-build" --output-dir "${RC_WORK}/base-payload" --auth-file "${auth}" || return
  else
    rc_engine container base-images root-base publish --enforce-vulnerabilities="${RC_ADMIN_VULNERABILITIES:?}" \
      "${rc_base_args[@]}" \
      --layouts-dir "${CI_PROJECT_DIR}/_release-base-build" --output-dir "${RC_WORK}/base-payload" --auth-file "${auth}" || return
  fi
  rm -f -- "${auth}"
  rc_new_handoff "${handoff}" || return
  cp -R -- "${RC_WORK}/base-payload" "${handoff}/payload" || return
  rc_engine release dist-digest --canonical-payload --dist-dir "${handoff}/payload" >"${handoff}/digest"
}

# rc_base_verified looks for the stage's final tags with the registry
# credential and verifies signature, SBOM and lineage of each one found
# against the pinned key, the declared inputs $1 and this project's release
# workflow. It sets rc_base_found to all, none or some, and writes the
# verified projections to parent-bases.json.
rc_base_verified() {
  local outputs="${RC_WORK:?}/base-verified.env" auth="${RC_WORK}/registry-auth.json" all projections
  : >"${outputs}"
  rc_base_login "${auth}" || return
  CI_OUTPUT="${outputs}" rc_engine container base-images validate --auth-file "${auth}" \
    --flavors-file base-flavors --base-inputs-json "$1" \
    --expected-repository "${rc_base_repository:?}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
    --expected-source "${RC_SERVER_URL}/${CI_PROJECT_PATH}" --expected-workflow gitlab/release.yml \
    --cosign-public-key-path trusted.pub \
    --cosign-public-key-sha256 "${RC_PUBLIC_KEY_SHA256:?}" || return
  rm -f -- "${auth}"
  all=$(rc_output "${outputs}" ALL_FOUND) || return
  projections=$(rc_output "${outputs}" PARENT_BASES_JSON) || return
  printf '%s\n' "${projections}" >parent-bases.json
  rc_base_found=some
  if [ "${all}" = true ]; then rc_base_found=all; elif [ "${projections}" = '{}' ]; then rc_base_found=none; fi
}

# rc_base_promote signs the stage's candidates and promotes them, the one
# base job that holds the key. Promotion verifies each signature, SBOM and
# lineage against the pinned key before a final tag moves. A stage whose
# final tags an earlier release promoted is reused instead: nothing is
# signed, and the same verification stands for it. A stage of which only
# some final tags exist is one whose promotion stopped between two tags:
# the job signs nothing and promotes again, which completes the stage only
# when every candidate already carries a signature that verifies and each
# existing final tag is the digest this stage staged, and refuses it
# otherwise before a tag moves. Either way the job hands on each flavor's
# verified {ref,input_id}, the only source of a child's parent.
rc_base_promote() {
  local stage=$1 parent=$2 handoff="${CI_PROJECT_DIR}/_release-base-promote" ledger inputs started flavor
  local -a flavors
  # The signing environment's variables arrive together; bases take no GPG
  # key, sign no distro package and no release file, so the preflight asks
  # for none.
  unset RELEASE_GPG_PRIVATE_KEY RELEASE_GPG_PASSPHRASE
  rc_base_stage "${stage}" "${parent}" || return
  rc_signing_preflight false cosign || return
  rc_release_checkout || return
  started=$(git log -1 --format=%cI) || return
  cd -- "${RC_WORK}" || return
  rc_read_handoff "${CI_PROJECT_DIR}/_release-base-publish" canonical-payload || return
  ledger="${DIST_DIR}/base-images.json"
  rc_require_regular_file "${ledger}" || return
  # The publishing job's ledger declares the complete expected map: every
  # flavor's input ID and a child's parent_base, required in each signed
  # statement.
  inputs=$(rc_engine container base-images declared-inputs --ledger "${ledger}" \
    --expected-repository "${rc_base_repository}") || return
  IFS=+ read -r -a flavors <<<"${stage}"
  printf '%s\n' "${flavors[@]}" >base-flavors
  rc_base_verified "${inputs}" || return
  case "${rc_base_found}" in
  all) echo "base stage ${stage}: already promoted and verified; reused." ;;
  none | some)
    if [ "${rc_base_found}" = none ]; then
      REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
        COSIGN_KEY="${RELEASE_COSIGN_PRIVATE_KEY:?}" COSIGN_PASSWORD="${RELEASE_COSIGN_PASSPHRASE:?}" \
        rc_engine container base-images sign --ledger "${ledger}" --evidence-root verified-payload \
        --expected-repository "${rc_base_repository}" \
        --registry "${RC_REGISTRY}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
        --workflow gitlab/release.yml --source-sha "${RELEASE_SHA}" --started-on "${started}" \
        --sbom-path-pattern '^premade-sboms/base-sbom-[A-Za-z0-9._-]+[.]cyclonedx[.]json$' \
        --method kms --key env://COSIGN_KEY || return
    else
      echo "base stage ${stage}: some final tags exist; completing the promotion of what this stage signed."
    fi
    if ! REGISTRY_USER="${RELEASE_REGISTRY_USER:?}" REGISTRY_TOKEN="${RELEASE_REGISTRY_TOKEN:?}" \
      rc_engine container base-images promote --ledger "${ledger}" --base-inputs-json "${inputs}" \
      --expected-repository "${rc_base_repository}" \
      --registry "${RC_REGISTRY}" --server-url "${RC_SERVER_URL}" --repository "${CI_PROJECT_PATH}" \
      --expected-source "${RC_SERVER_URL}/${CI_PROJECT_PATH}" --expected-workflow gitlab/release.yml \
      --cosign-public-key-path trusted.pub \
      --cosign-public-key-sha256 "${RC_PUBLIC_KEY_SHA256}"; then
      if [ "${rc_base_found}" = some ]; then
        echo "ERROR: some flavors of the stage ${stage} are already promoted, and they are not what this stage signed. A stage is promoted or reused whole: give a flavor that changes on its own a stage of its own in base-stages." >&2
      fi
      return 1
    fi
    rc_base_verified "${inputs}" || return
    if [ "${rc_base_found}" != all ]; then
      echo "ERROR: the promoted stage ${stage} does not verify under its final tags." >&2
      return 1
    fi
    ;;
  esac
  rc_new_handoff "${handoff}" || return
  for flavor in "${flavors[@]}"; do
    rc_engine container base-images select-parent --parent-bases-file parent-bases.json \
      --flavor "${flavor}" >"${handoff}/${flavor}.json" || return
    printf 'base %s: ' "${flavor}"
    cat -- "${handoff}/${flavor}.json"
  done
}

# rc_base_gate hands every promoted flavor's {ref,input_id} on to the
# release files job, which waits for it: the release's files are built only
# after every base of the policy is promoted. It holds no secret.
rc_base_gate() {
  local handoff="${CI_PROJECT_DIR}/_release-bases" flavor
  rc_new_handoff "${handoff}" || return
  while IFS= read -r flavor; do
    rc_require_regular_file "${CI_PROJECT_DIR}/_release-base-promote/${flavor}.json" || return
    cp -- "${CI_PROJECT_DIR}/_release-base-promote/${flavor}.json" "${handoff}/${flavor}.json" || return
  done < <(rc_base_flavors)
}

# rc_gather_bases holds the release files job to the policy's bases: every
# flavor promoted, or no base hand-off at all. The build commands read each
# flavor's {ref,input_id} from FORGE_CI_BASE_IMAGES_DIR/<flavor>.json.
rc_gather_bases() {
  local directory="${CI_PROJECT_DIR}/_release-bases" flavor
  if [ -z "${RC_BASE_STAGES?}" ]; then
    rc_require_absent "${directory}"
    return
  fi
  while IFS= read -r flavor; do
    rc_require_regular_file "${directory}/${flavor}.json" || return
  done < <(rc_base_flavors)
  export FORGE_CI_BASE_IMAGES_DIR="${directory}"
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
  # Read the release back with the job token: the command only reads, so the
  # release token, and every other release secret, is out of its environment.
  (
    unset RELEASE_SSH_SIGNING_KEY RELEASE_COSIGN_PRIVATE_KEY RELEASE_COSIGN_PASSPHRASE \
      RELEASE_GPG_PRIVATE_KEY RELEASE_GPG_PASSPHRASE RELEASE_TOKEN RELEASE_REGISTRY_TOKEN \
      SSH_SIGNING_KEY COSIGN_KEY COSIGN_PASSWORD GPG_SIGNING_KEY GPG_SIGNING_PASSWORD GITLAB_TOKEN
    rc_engine release validate-published --tag "${RELEASE_TAG}" \
      --repository "${CI_PROJECT_PATH}" --draft=false --manifest "${RELEASE_FILES_MANIFEST}" --dist-dir "${DIST_DIR}"
  )
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
  # The plan names the family jobs that ran; their files stay where they are.
  rc_checkout "${RELEASE_REQUEST_SHA:?}" || return
  rc_planned_jobs "${RC_WORK:?}/planned-jobs" || return
  for name in "Check: signing access" "Check: publishing access" "Check: registry access" \
    "Check: release request" "Check: consumer checks" "Prepare: changelog" "Prepare: version java" \
    "Prepare: version node" "Prepare: version rust" "Prepare: release commit and tag" "Build: java" \
    "Build: node" "Build: go" "Build: rust" "Build: android" "Build: release files" "Images: plan" "Images: build amd64" \
    "Images: build arm64" "Images: publish" "Sign: files and images" \
    "Publish: images" "Publish: release" "Publish: tidy up" "Publish: npm packages"; do
    # A family job, and the package job, ran when the plan named it.
    case "${name}" in
    "Build: release files") ;;
    "Prepare: version "* | "Build: "* | "Publish: npm packages") if ! grep -qxF "${name}" "${RC_WORK}/planned-jobs"; then continue; fi ;;
    esac
    if [ "${name}" = "Check: registry access" ] && [ "${images}" != true ] && [ -z "${RC_BASE_STAGES?}" ]; then continue; fi
    case "${name}" in "Images: "*) if [ "${images}" != true ]; then continue; fi ;; esac
    if [ "${name}" = "Images: build arm64" ] && [ "${arm64_tags}" = '[]' ]; then continue; fi
    jobs+=(--job "${name}=success")
  done
  # The base jobs are the plan's as well, each named after its stage.
  while IFS= read -r name; do
    case "${name}" in "Base: "*) jobs+=(--job "${name}=success") ;; esac
  done <"${RC_WORK}/planned-jobs"
  rc_engine report release --release-version "${RELEASE_TAG:?}" --release-branch "${RC_RELEASE_BRANCH:?}" \
    --release-actor "${GITLAB_USER_LOGIN:-}" "${jobs[@]}"
}
