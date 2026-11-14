#!/bin/bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Protected canonical workflow glue. Transport is the pinned native
# Actions upload/download pair, never a forge-ci command or curl fallback.
set -euo pipefail
export PATH=/usr/bin:/bin
unset FORGE_CI_BIN FORGE_CI_PATH FORGE_CI_BINARY_REF FORGE_CI_PLAN
fail() {
  printf 'canonical-cli-delivery: %s\n' "$1" >&2
  exit 78
}
[[ $# == 1 ]] || fail 'expected build, prepare, admit or receive'
mode=$1
[[ "$mode" == build || "$mode" == prepare || "$mode" == admit || "$mode" == receive ]] || fail 'unknown operation'
[[ "${ENGINE_REPOSITORY:-}" =~ ^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$ ]] || fail 'explicit approved engine repository required'
[[ "${ENGINE_COMMIT:-}" =~ ^[0-9a-f]{40}$ ]] || fail 'full approved engine commit required'
[[ "${ENGINE_ORIGIN:-}" == "${GITHUB_SERVER_URL:?}/$ENGINE_REPOSITORY" && "$GITHUB_SERVER_URL" =~ ^https://[^/?#@[:space:]]+$ ]] || fail 'engine origin must be on the approved GitHub server'
[[ "${RUNTIME_IMAGE:-}" =~ ^[a-z0-9][a-z0-9./:_-]*@sha256:[0-9a-f]{64}$ ]] || fail 'approved image digest required'
for value in "${GITHUB_RUN_ID:-}" "${GITHUB_RUN_ATTEMPT:-}" "${GITHUB_REPOSITORY_ID:-}"; do
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || fail 'native current-run context is missing'
done
[[ "${GITHUB_SHA:-}" =~ ^[0-9a-f]{40}$ ]] || fail 'native run source commit missing'
: "${DELIVERY_DIRECTORY:=${RUNNER_TEMP:?}/forge-ci-canonical-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT}"
: "${ARTIFACT_METADATA:=$RUNNER_TEMP/forge-ci-canonical-metadata.json}"
[[ "${ENGINE_CHECKOUT:-}" == /* && -d "$ENGINE_CHECKOUT" && "$(realpath -e -- "$ENGINE_CHECKOUT")" == "$ENGINE_CHECKOUT" ]] || fail 'canonical engine checkout path required'
[[ "${DELIVERY_DIRECTORY:-}" == /* && "${RUNNER_TEMP:-}" == /* && "$DELIVERY_DIRECTORY" == "$RUNNER_TEMP/forge-ci-canonical-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT" ]] || fail 'unexpected delivery directory'
artifact_name=forge-ci-canonical-linux-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT-$ENGINE_COMMIT
# One delivery carries the engine's two recorded Linux builds; a receiver
# runs the file of its own architecture and admits every delivered byte.
architectures=(amd64 arm64)

# Fresh authenticated checkout/local object storage and protected no-writer
# runner are prerequisites, not facts authenticated by these local assertions.
engine_git() {
  env -i PATH=/usr/bin:/bin HOME=/nonexistent GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_NO_LAZY_FETCH=1 \
    git --no-pager --no-replace-objects -C "$ENGINE_CHECKOUT" "$@"
}
[[ "$(engine_git rev-parse --show-toplevel)" == "$ENGINE_CHECKOUT" && "$(engine_git rev-parse --verify HEAD)" == "$ENGINE_COMMIT" ]] || fail 'wrong engine checkout'
origin=$(engine_git config --local --no-includes --get remote.origin.url) || fail 'missing engine origin'
[[ "${origin%.git}" == "$ENGINE_ORIGIN" ]] || fail 'wrong engine origin'
files=(scripts/ci/engine-binary.sh .engine/bin/forge-ci-linux-amd64.sha256 .engine/bin/forge-ci-linux-arm64.sha256 scripts/ci/trusted-runtime-tools.sh)
if [[ "$mode" == build ]]; then
  files+=(go.mod go.sum)
else
  files+=(scripts/ci/trusted-bootstrap.sh .engine/bin/lib-forge-ci.sh)
fi
for relative in "${files[@]}"; do
  file=$ENGINE_CHECKOUT/$relative
  [[ -f "$file" && ! -L "$file" && "$(realpath -e -- "$file")" == "$file" ]] || fail "missing/unsafe engine file: $relative (queued trusted-bootstrap integration is required)"
  tree_entry=$(engine_git ls-tree "$ENGINE_COMMIT" -- "$relative")
  [[ "$tree_entry" == '100644 blob '* || "$tree_entry" == '100755 blob '* ]] || fail "not a committed regular file: $relative"
  [[ "$(engine_git hash-object --no-filters -- "$file")" == "$(engine_git rev-parse "$ENGINE_COMMIT:$relative")" ]] || fail "modified engine file: $relative"
done

if [[ "$mode" == build ]]; then
  [[ "${GO:-}" == /* && -x "$GO" ]] || fail 'approved absolute Go compiler required'
  [[ ! -e "$DELIVERY_DIRECTORY" && ! -L "$DELIVERY_DIRECTORY" ]] || fail 'delivery directory already exists'
  # Only the approved engine is present. Download module DATA, not consumer
  # hooks/packages, with standard TLS and Go checksum-database verification.
  # No inherited credentials, Go config, workspace, proxy or cache is admitted.
  # Full source (including any local replacements) is an admin selection and
  # pristine authenticated checkout prerequisite, not authenticated by Go sums.
  module_work=$(mktemp -d "$RUNNER_TEMP/forge-ci-canonical-modules.XXXXXXXX")
  trap 'chmod -R u+w -- "$module_work"; rm -rf -- "$module_work"' EXIT
  mkdir -m0700 -- "$module_work/modules" "$module_work/build" "$module_work/tmp"
  export GOMODCACHE=$module_work/modules GOCACHE=$module_work/build
  # No home: Go's telemetry child outlives the go command and would write
  # its counters below HOME after this directory is removed.
  module_env=(env -i PATH=/usr/bin:/bin HOME=/nonexistent
    GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS= CGO_ENABLED=0
    GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
    "GOMODCACHE=$GOMODCACHE" "GOCACHE=$GOCACHE" "TMPDIR=$module_work/tmp"
    GOMAXPROCS=2 GOOS=linux)
  pinned=go$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' "$ENGINE_CHECKOUT/go.mod")
  [[ "$("${module_env[@]}" "$GO" env GOVERSION)" == "$pinned" ]] || fail 'wrong preparation toolchain'
  # With Go >=1.17, the no-argument command selects the main module's
  # explicit requirements (including indirect package providers). "all"
  # instead fetches unrelated graph modules and records extra checksums.
  "${module_env[@]}" "$GO" -C "$ENGINE_CHECKOUT" mod download
  # Download must not repair an incomplete selected lock file. Missing/changed
  # sums fail before ANY build/output, even if Go authenticated their retrieval.
  for relative in go.mod go.sum; do
    [[ "$(engine_git hash-object --no-filters -- "$ENGINE_CHECKOUT/$relative")" == "$(engine_git rev-parse "$ENGINE_COMMIT:$relative")" ]] || fail "module preparation changed $relative"
  done
  mkdir -m0700 -- "$DELIVERY_DIRECTORY"
  for arch in "${architectures[@]}"; do
    /bin/bash "$ENGINE_CHECKOUT/scripts/ci/engine-binary.sh" build "$arch" "$DELIVERY_DIRECTORY/forge-ci-linux-$arch"
    /bin/bash "$ENGINE_CHECKOUT/scripts/ci/engine-binary.sh" verify "$arch" "$DELIVERY_DIRECTORY/forge-ci-linux-$arch"
  done
  # Capture ORIGINAL producer identity here, never in a later wrapper job.
  {
    printf 'artifact-name=%s\nrun-id=%s\nrun-attempt=%s\n' "$artifact_name" "$GITHUB_RUN_ID" "$GITHUB_RUN_ATTEMPT"
    printf 'engine-repository=%s\nengine-commit=%s\n' "$ENGINE_REPOSITORY" "$ENGINE_COMMIT"
  } >>"${GITHUB_OUTPUT:?}"
  exit 0
fi

[[ "${PRODUCER_ARTIFACT_ID:-}" =~ ^[1-9][0-9]*$ ]] || fail 'one original upload artifact ID required'
[[ "${PRODUCER_RUN_ID:-}" == "$GITHUB_RUN_ID" && "${PRODUCER_RUN_ATTEMPT:-}" == "$GITHUB_RUN_ATTEMPT" ]] || fail 'stale producer: rerun the whole chain in the current attempt'
[[ "${PRODUCER_ENGINE_REPOSITORY:-}" == "$ENGINE_REPOSITORY" && "${PRODUCER_ENGINE_COMMIT:-}" == "$ENGINE_COMMIT" ]] || fail 'producer engine differs from approval'
[[ "${PRODUCER_ARTIFACT_NAME:-}" == "$artifact_name" ]] || fail 'producer name is not the independently computed current-attempt name'
if [[ "$mode" == prepare ]]; then
  [[ ! -e "$DELIVERY_DIRECTORY" && ! -L "$DELIVERY_DIRECTORY" ]] || fail 'delivery directory already exists'
  mkdir -m0700 -- "$DELIVERY_DIRECTORY"
  exit 0
fi

[[ "${ARTIFACT_METADATA:-}" == "$RUNNER_TEMP/forge-ci-canonical-metadata.json" && -f "$ARTIFACT_METADATA" && ! -L "$ARTIFACT_METADATA" ]] || fail 'native artifact metadata missing'
[[ "$(stat -c %s -- "$ARTIFACT_METADATA")" -le 65536 ]] || fail 'oversized metadata'
# GitHub records a pull_request run under the pull request's head commit;
# GITHUB_SHA names that run's temporary merge commit instead.
run_head_sha=$GITHUB_SHA
if [[ "${GITHUB_EVENT_NAME:-}" == pull_request ]]; then
  event=${GITHUB_EVENT_PATH:-}
  [[ "$event" == /* && -f "$event" && ! -L "$event" ]] || fail 'native pull request event missing'
  run_head_sha=$(jq -er '.pull_request.head.sha | strings' "$event") || fail 'pull request head commit missing'
  [[ "$run_head_sha" =~ ^[0-9a-f]{40}$ ]] || fail 'pull request head commit missing'
fi
# Native GitHub metadata only; no new manifest/trust schema or transport client.
jq -e --arg id "$PRODUCER_ARTIFACT_ID" --arg name "$artifact_name" \
  --arg run "$GITHUB_RUN_ID" --arg repo "$GITHUB_REPOSITORY_ID" --arg sha "$run_head_sha" '
	type == "object" and (.id | tostring) == $id and .name == $name and .expired == false
	and (.workflow_run.id | tostring) == $run and (.workflow_run.repository_id | tostring) == $repo
	and .workflow_run.head_sha == $sha
	and (.size_in_bytes | type == "number" and . > 0 and . <= 134217728 and floor == .)
' "$ARTIFACT_METADATA" >/dev/null || fail 'foreign/stale/oversized artifact metadata'
[[ -d "$DELIVERY_DIRECTORY" && ! -L "$DELIVERY_DIRECTORY" && "$(realpath -e -- "$DELIVERY_DIRECTORY")" == "$DELIVERY_DIRECTORY" ]] || fail 'unsafe delivery directory'
shopt -s nullglob dotglob
entries=("$DELIVERY_DIRECTORY"/*)
if [[ "$mode" == admit ]]; then
  [[ ${#entries[@]} == 0 ]] || fail 'download destination is not empty'
  exit 0
fi
[[ "${TOOL_PROFILE:-}" =~ ^[a-z][a-z-]*$ ]] || fail 'tool profile literal required for the verified probe'
case "$(uname -s)/$(uname -m)" in
Linux/x86_64) host_arch=amd64 ;;
Linux/aarch64) host_arch=arm64 ;;
*) fail 'the canonical CLI runs natively on linux/amd64 or linux/arm64 only' ;;
esac
[[ ${#entries[@]} -eq ${#architectures[@]} ]] || fail 'delivery requires exactly one canonical regular file per Linux architecture, no sidecars'
for arch in "${architectures[@]}"; do
  binary=$DELIVERY_DIRECTORY/forge-ci-linux-$arch
  [[ -f "$binary" && ! -L "$binary" ]] || fail "delivery is missing the regular linux/$arch file"
  [[ "$(stat -c %s -- "$binary")" -gt 0 && "$(stat -c %s -- "$binary")" -le 134217728 ]] || fail 'delivery exceeds canonical binary bounds'
  # No helper/CLI process before canonical hash admission of every file.
  /bin/bash "$ENGINE_CHECKOUT/scripts/ci/engine-binary.sh" verify "$arch" "$binary"
done
binary=$DELIVERY_DIRECTORY/forge-ci-linux-$host_arch
# Native Actions may restore mode 0644; set executable only after the byte
# verification above, and only on the file this host runs.
chmod 0755 -- "$binary"
exec /bin/bash "$ENGINE_CHECKOUT/scripts/ci/trusted-bootstrap.sh" \
  "$ENGINE_CHECKOUT" "$ENGINE_ORIGIN" "$ENGINE_COMMIT" "$binary" "$TOOL_PROFILE" -- --version
