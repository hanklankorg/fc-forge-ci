#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# Shared resolver for the delivery layers' forge-ci binary. The CI behavior
# lives in the binary; this file only owns the layer's pin and
# signing-boundary policy.
#
# The checkout tracks one line per architecture, bin/forge-ci-linux-<arch>.sha256,
# and not the binary. A job takes the line of its host's architecture and
# downloads the binary by that digest from the generic packages of the forge
# that serves the layer, without credentials. The storage is not
# trusted: the layer's binary, and under SIGNER_STRICT=1 any FORGE_CI_BIN
# override, must match the recorded checksum before it runs, including a
# caller's version probe. A non-strict override is an internal test and
# development seam. The check runs once per shell; the binary then runs in
# place. See "The binary in the layer" in docs/forgejo.md.
#
# Exit statuses: 69 only when the download host could not be reached, which
# another attempt may pass; 1 when a binary is not the recorded one, which is
# never retried; 78 for everything this job lacks or has set up wrongly.

# The engine root, .engine/ of this checkout. Paths in messages and in the
# checksum file are relative to it. Resolved ONCE at source time: BASH_SOURCE
# may be a relative path, so it must be anchored while the sourcing shell is
# still in the repo. Callers may change directories before invoking a helper.
HELPERS_ROOT=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
HELPERS_ENGINE_VERIFIED=
HELPERS_ENGINE_SHA256=
HELPERS_ENGINE_RECORDED=
HELPERS_ENGINE_ARCH=

# The largest download accepted. The binary is about 25 MB.
HELPERS_ENGINE_MAX_BYTES=134217728

# _forge_ci_arch sets HELPERS_ENGINE_ARCH to the host's architecture, as the
# binary's name spells it. The binary runs natively or not at all: a host of
# another architecture would run it under emulation, if at all, so it is
# refused.
_forge_ci_arch() {
  local machine
  machine=$(uname -m) || machine=
  case "${machine}" in
  x86_64 | amd64) HELPERS_ENGINE_ARCH=amd64 ;;
  aarch64 | arm64) HELPERS_ENGINE_ARCH=arm64 ;;
  *)
    echo "ERROR: the forge-ci binary is built for amd64 and arm64 hosts, and this host is '${machine}'; use a native host of one of those architectures" >&2
    return 78
    ;;
  esac
}

# _forge_ci_recorded reads the ONE recorded pin for the host architecture's
# binary into HELPERS_ENGINE_RECORDED. It ships in the same SHA-verified
# checkout as the workflows, so an inline per-workflow copy would add nothing
# except a second value that can drift.
_forge_ci_recorded() {
  local checksum_path recorded='' name='' extra=''
  local -a lines=()

  _forge_ci_arch || return
  checksum_path=${HELPERS_ROOT}/bin/forge-ci-linux-${HELPERS_ENGINE_ARCH}.sha256

  if [ -f "${checksum_path}" ] && [ ! -L "${checksum_path}" ]; then
    mapfile -t lines <"${checksum_path}"
    read -r recorded name extra <<<"${lines[0]:-}"
  fi
  if [ "${#lines[@]}" -ne 1 ] || [[ ! "${recorded}" =~ ^[0-9a-f]{64}$ ]] ||
    [ "${name}" != bin/forge-ci ] || [ -n "${extra}" ]; then
    echo "ERROR: recorded forge-ci checksum is missing or malformed: ${checksum_path}" >&2
    return 78
  fi
  HELPERS_ENGINE_RECORDED=${recorded}
}

# _forge_ci_local_path prints where the layer's binary is in this job.
#
# An action the runner fetched by URL has a fresh directory in every step, so
# nothing written beside it reaches the next step: its binary goes to the
# job's temporary directory, named by the digest. Everywhere else this file
# was sourced from a checkout the job made, and the binary goes beside the
# checksum, where the checkout's later steps and the signer toolchain check
# find it.
_forge_ci_local_path() {
  if [ -n "${GITHUB_ACTION_REPOSITORY:-}" ]; then
    printf '%s/forge-ci-%s/forge-ci\n' "${RUNNER_TEMP:-${TMPDIR:-/tmp}}" "$1"
  else
    printf '%s/bin/forge-ci\n' "${HELPERS_ROOT}"
  fi
}

# forge_ci_package_url DIGEST ARCH prints the address of the binary with
# that digest, built for ARCH (amd64 or arm64):
#
#   <forge>/api/packages/<engine owner>/generic/forge-ci/<sha256>/forge-ci-linux-<arch>
# or, with FORGE_CI_PACKAGE_PROVIDER=gitlab:
#   <CI_SERVER_URL>/api/v4/projects/<encoded project>/packages/generic/forge-ci/<sha256>/forge-ci-linux-<arch>
#
# The owner is the owner of the repository that serves the layer, never the
# consumer's. Two rules name that repository, and they cover every context:
#
#   1. In an action the runner fetched by URL, the runner says where from:
#      GITHUB_SERVER_URL and GITHUB_ACTION_REPOSITORY.
#   2. Otherwise this file was sourced from a checkout a workflow made, and
#      that workflow holds the repository it cloned: FORGE_CI_REPOSITORY_URL.
#
# Nothing is read from Git: the runner's copy of an action is not a usable
# repository.
forge_ci_package_url() {
  local digest=$1 arch=${2:-} repository owner root project

  case "${arch}" in
  amd64 | arm64) ;;
  *)
    echo "ERROR: the forge-ci binary is built for amd64 and arm64, not '${arch}'" >&2
    return 78
    ;;
  esac

  if [ -n "${GITHUB_ACTION_REPOSITORY:-}" ]; then
    repository=${GITHUB_SERVER_URL:-}
    repository=${repository%/}/${GITHUB_ACTION_REPOSITORY}
  elif [ -n "${FORGE_CI_REPOSITORY_URL:-}" ]; then
    repository=${FORGE_CI_REPOSITORY_URL}
  else
    echo "ERROR: cannot tell which repository serves the forge-ci layer, so its binary cannot be downloaded: this step is no action fetched by URL, and FORGE_CI_REPOSITORY_URL is not set. A workflow that checks the layer out itself sets FORGE_CI_REPOSITORY_URL to that repository's HTTPS URL." >&2
    return 78
  fi
  repository=${repository%/}
  repository=${repository%.git}
  case "${FORGE_CI_PACKAGE_PROVIDER:-forgejo}" in
  gitlab)
    # GitLab components are included from the consumer's own instance.
    # Its registry is project-scoped, including every subgroup in the path.
    root=${CI_SERVER_URL:-}
    root=${root%/}
    project=${repository#"${root}/"}
    if [[ ! "${root}" =~ ^https://[^?#@[:space:]]+$ || "${project}" == "${repository}" ||
      ! "${project}" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)+$ ||
      ! "${digest}" =~ ^[0-9a-f]{64}$ ]]; then
      echo "ERROR: GitLab helpers must name a project on CI_SERVER_URL and a lowercase SHA-256 binary digest" >&2
      return 78
    fi
    printf '%s/api/v4/projects/%s/packages/generic/forge-ci/%s/forge-ci-linux-%s\n' \
      "${root}" "${project//\//%2F}" "${digest}" "${arch}"
    return 0
    ;;
  forgejo) ;;
  *)
    echo "ERROR: FORGE_CI_PACKAGE_PROVIDER must be forgejo or gitlab" >&2
    return 78
    ;;
  esac
  root=${repository%/*}
  owner=${root##*/}
  root=${root%/*}
  if [[ ! "${repository}" =~ ^https://[^/?#@[:space:]]+(/[^?#@[:space:]]+)?/[^/?#@[:space:]]+/[^/?#@[:space:]]+$ ]] ||
    [[ ! "${owner}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || [[ ! "${digest}" =~ ^[0-9a-f]{64}$ ]]; then
    echo "ERROR: the repository that serves the forge-ci layer must be an HTTPS URL ending in owner/repository, without credentials, query or fragment: ${repository}" >&2
    return 78
  fi
  printf '%s/api/packages/%s/generic/forge-ci/%s/forge-ci-linux-%s\n' "${root}" "${owner}" "${digest}" "${arch}"
}

# download_forge_ci URL SHA256 DESTINATION downloads over HTTPS without
# credentials into a temporary file beside the destination, and moves it into
# place only when it is the bytes the digest names.
download_forge_ci() {
  local url=$1 digest=$2 destination=$3
  local directory=${destination%/*} partial actual status

  if ! command -v curl >/dev/null 2>&1 || ! command -v sha256sum >/dev/null 2>&1; then
    echo "ERROR: curl and sha256sum are needed to download the forge-ci binary: ${url}" >&2
    return 78
  fi
  if ! partial=$(mktemp "${directory}/.forge-ci.XXXXXX"); then
    echo "ERROR: cannot write the forge-ci binary to ${directory} (downloading ${url})" >&2
    return 78
  fi
  status=0
  curl --proto '=https' --proto-redir '=https' --fail --location --silent --show-error \
    --retry 3 --connect-timeout 30 --max-time 600 \
    --max-filesize "${HELPERS_ENGINE_MAX_BYTES}" \
    --output "${partial}" -- "${url}" || status=$?
  if [ "${status}" -ne 0 ]; then
    rm -f -- "${partial}"
    echo "ERROR: cannot download the forge-ci binary (curl exit ${status}): ${url}" >&2
    if [ "${FORGE_CI_PACKAGE_PROVIDER:-forgejo}" = gitlab ]; then
      echo "  Publish the recorded build to the engine project's public generic package registry with scripts/ci/engine-binary.sh publish and FORGE_CI_PACKAGE_PROVIDER=gitlab; see docs/gitlab.md." >&2
    else
      echo "  The binary is a generic package of the owner of the repository that serves the layer, named by the sha256 the pinned commit records, and must be readable without credentials." >&2
      echo "  That owner's signer-image workflow (examples/forgejo/signer-image.yml) publishes it: its first job builds the binary of the commit it pins and uploads it. Run it at this commit, then run this job again." >&2
    fi
    # Only a host that could not be reached is worth another attempt of the
    # job: curl could not resolve it (5, 6), connect to it (7), or keep the
    # transfer going (18, 28, 52, 55, 56). An HTTP status (22), a refused
    # certificate and every other exit are answers or this job's set-up.
    case "${status}" in
    5 | 6 | 7 | 18 | 28 | 52 | 55 | 56) return 69 ;;
    esac
    return 78
  fi
  actual=$(sha256sum -- "${partial}") || actual=
  actual=${actual%% *}
  if [ "${actual}" != "${digest}" ]; then
    rm -f -- "${partial}"
    echo "ERROR: the downloaded forge-ci binary is not the one the pinned commit records: ${url}" >&2
    echo "  recorded: ${digest}" >&2
    echo "  actual:   ${actual}" >&2
    return 1
  fi
  if ! chmod 0755 -- "${partial}" || ! mv -f -- "${partial}" "${destination}"; then
    rm -f -- "${partial}"
    echo "ERROR: cannot put the forge-ci binary in place at ${destination} (downloaded ${url})" >&2
    return 78
  fi
}

# fetch_forge_ci makes the layer's binary present in this job: it does
# nothing when the file is there with the recorded digest, and downloads it
# otherwise. It runs nothing and reads no credential, so a signer job calls it
# in a step of its own before the toolchain check and before any secret.
fetch_forge_ci() {
  local binary directory actual url

  _forge_ci_recorded || return
  binary=$(_forge_ci_local_path "${HELPERS_ENGINE_RECORDED}")
  if [ -f "${binary}" ] && [ ! -L "${binary}" ]; then
    actual=$(sha256sum -- "${binary}") || actual=
    [ "${actual%% *}" != "${HELPERS_ENGINE_RECORDED}" ] || return 0
  fi
  url=$(forge_ci_package_url "${HELPERS_ENGINE_RECORDED}" "${HELPERS_ENGINE_ARCH}") || return
  directory=${binary%/*}
  # The directory holds an executable: it is the job's own, and no link.
  # shellcheck disable=SC2174 # The mode is meant for the deepest directory alone.
  if ! mkdir -p -m 0700 -- "${directory}" || [ -L "${directory}" ] || [ ! -O "${directory}" ]; then
    echo "ERROR: the directory for the forge-ci binary is not this job's own: ${directory} (downloading ${url})" >&2
    return 78
  fi
  download_forge_ci "${url}" "${HELPERS_ENGINE_RECORDED}" "${binary}"
}

# verify_forge_ci checks the selected binary and records its path in
# HELPERS_ENGINE_VERIFIED, and its digest in HELPERS_ENGINE_SHA256 when the
# checksum applies. The layer's own binary is downloaded first when the job
# does not have it yet, except under SIGNER_STRICT=1: a privileged job fetches
# in its explicit step, and a missing binary later is an error there.
verify_forge_ci() {
  local binary=${FORGE_CI_BIN:-} actual

  if [ -n "${HELPERS_ENGINE_VERIFIED}" ] && [ "${HELPERS_ENGINE_VERIFIED}" = "${binary}" ]; then
    return 0
  fi
  if [ -z "${binary}" ] || [ "${SIGNER_STRICT:-0}" = 1 ]; then
    _forge_ci_recorded || return
  fi
  if [ -z "${binary}" ]; then
    binary=$(_forge_ci_local_path "${HELPERS_ENGINE_RECORDED}")
    if [ -n "${HELPERS_ENGINE_VERIFIED}" ] && [ "${HELPERS_ENGINE_VERIFIED}" = "${binary}" ]; then
      return 0
    fi
    if [ ! -e "${binary}" ] && [ "${SIGNER_STRICT:-0}" != 1 ]; then
      fetch_forge_ci || return
    fi
  fi
  if [ ! -f "${binary}" ] || [ -L "${binary}" ] || [ ! -x "${binary}" ]; then
    echo "ERROR: forge-ci binary is missing, a symlink, or not executable: ${binary}" >&2
    return 78
  fi

  if [ -z "${FORGE_CI_BIN:-}" ] || [ "${SIGNER_STRICT:-0}" = 1 ]; then
    if ! actual=$(sha256sum -- "${binary}"); then
      echo "ERROR: cannot checksum forge-ci binary: ${binary}" >&2
      return 78
    fi
    actual=${actual%% *}
    if [ "${actual}" != "${HELPERS_ENGINE_RECORDED}" ]; then
      echo "ERROR: forge-ci binary does not match the recorded pin (bin/forge-ci-linux-${HELPERS_ENGINE_ARCH}.sha256)" >&2
      echo "  recorded: ${HELPERS_ENGINE_RECORDED}" >&2
      echo "  actual:   ${actual}" >&2
      return 1
    fi
    # shellcheck disable=SC2034 # Read by setup-forge-ci.sh.
    HELPERS_ENGINE_SHA256=${actual}
  fi
  HELPERS_ENGINE_VERIFIED=${binary}
}

run_forge_ci() {
  verify_forge_ci || return
  "${HELPERS_ENGINE_VERIFIED}" "$@"
}

# forge_ci_admin_path DIR LINT VULNERABILITIES APPLICATION_TESTS REPRODUCIBLE_BUILDS
# writes a forge-ci that runs the verified engine with the four settings as
# its flags, puts it first on PATH and points FORGE_CI_BIN at it, so the
# commands a job runs from its inputs (the caller's own, or the consumer
# scripts those name) gate under these settings whichever spelling they use.
# Each value is exactly true or false. The engine refuses the same flag given
# twice, so a command cannot replace a setting through this forge-ci. Call it
# after verify_forge_ci, which checksums the bare binary, and after the last
# change to PATH. Code that runs in the job can still reach the bare binary
# (HELPERS_ENGINE_VERIFIED names it); this narrows the ways, it is not a
# boundary against the job's own code.
forge_ci_admin_path() {
  local dir=${1:-} engine=${HELPERS_ENGINE_VERIFIED:-} value
  if [ "$#" -ne 5 ] || [ -z "${dir}" ] || [ -z "${engine}" ]; then
    echo "ERROR: forge_ci_admin_path needs a verified engine, a directory and four settings" >&2
    return 64
  fi
  for value in "${@:2}"; do
    case "${value}" in
    true | false) ;;
    *)
      echo "ERROR: an administrator setting is '${value}', not true or false" >&2
      return 64
      ;;
    esac
  done
  case "${dir}${engine}" in
  *"'"*)
    echo "ERROR: the engine path must not contain a single quote" >&2
    return 64
    ;;
  esac
  mkdir -p -- "${dir}" || return
  printf "#!/bin/sh\nexec '%s' --enforce-lint=%s --enforce-vulnerabilities=%s --enforce-application-tests=%s --enforce-reproducible-builds=%s \"\$@\"\n" \
    "${engine}" "$2" "$3" "$4" "$5" >"${dir}/forge-ci" || return
  chmod 0755 -- "${dir}/forge-ci" || return
  PATH="${dir}:${PATH}"
  FORGE_CI_BIN="${dir}/forge-ci"
  export PATH FORGE_CI_BIN
}
