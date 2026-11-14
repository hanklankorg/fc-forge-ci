#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Repository publication orchestration, not an engine CLI or consumer contract.
# Image operations belong to forge-ci; this script binds this runtime roster to
# the workflow's source commit, registry namespace, run and artifact names.
set -euo pipefail
roster=containers/runtime/images.json
fail() {
  printf 'runtime-images: %s\n' "$*" >&2
  exit 1
}

runtime_publication() {
  forge-ci container runtime-publication "$@" --roster "$roster" \
    --platform-matrix "${PLATFORM_MATRIX:?}" --image-namespace "${RUNTIME_IMAGE_NAMESPACE:?}" \
    --source-sha "${SOURCE_SHA:?}" --checked-out-sha "$(git rev-parse HEAD)" \
    --run-id "${RUNTIME_RUN_ID:?}" --publication-ref "${PUBLICATION_REF:?}"
}

download_metadata() {
  local target=$1 arch archs=$2
  while read -r arch; do
    forge-ci artifact download --name "runtime-$target-arch-$arch" --dir "runtime-$target-arch-$arch/"
  done <<<"$archs"
}

# The Go owner inspects retained bytes without running candidate tools and
# owns its temporary files independently of the scanner's authentication.
verify_runtime_cli() {
  forge-ci container runtime-publication verify-cli --arch "${1:?}" \
    --image-ref "${2:?}" --auth-file "${REGISTRY_AUTH_FILE:?}"
}

# A green job from an earlier attempt does not qualify rebuilt candidate bytes.
# Read the successful smoke job's receipt, not a newly computed smoke matrix.
verify_smoke() (
  target=${1:?} arch=${2:?} ref=${3:?}
  work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/runtime-smoke-check.XXXXXX")
  trap 'rm -rf -- "$work"' EXIT
  forge-ci artifact download --name "runtime-smoke-$target-$arch" --dir "$work/receipt"
  forge-ci container runtime-publication verify-smoke --receipt "$work/receipt/receipt.json" \
    --source-sha "${SOURCE_SHA:?}" --target "$target" --arch "$arch" --image-ref "$ref"
)

case "${1:-}" in
plan | collect)
  runtime_publication "$1"
  ;;
stage-cli)
  arch=${2:?}
  forge-ci container runtime-publication verify-cli --arch "$arch" --file "runtime-cli/forge-ci-$arch"
  mkdir -p cli-prebuilt
  install -m 0755 "runtime-cli/forge-ci-$arch" cli-prebuilt/forge-ci
  ;;
verify-cli)
  verify_runtime_cli "${2:?}" "${3:?}"
  ;;
verify-smoke)
  verify_smoke "${2:?}" "${3:?}" "${4:?}"
  ;;
build)
  target=${2:?} arch=${3:?}
  image=$(runtime_publication plan --target "$target" --arch "$arch")
  repo=$(jq -er .repository <<<"$image")
  tag=$(jq -er .tag <<<"$image")
  created=$(git show -s --format=%cI "$SOURCE_SHA")
  forge-ci container multiarch-image build-arch \
    --auth-file "${REGISTRY_AUTH_FILE:?}" --arch "$arch" \
    --source-sha "$SOURCE_SHA" --server-url "${FORGEJO_SERVER:?}" --repository "${FORGEJO_REPO:?}" \
    --image-repository "$repo" --tag "$tag" --name "runtime-$target" \
    --title "$(jq -r .name <<<"$image")" --containerfile containers/runtime/Containerfile --target "$target" \
    --label "org.opencontainers.image.url=$FORGEJO_SERVER/$FORGEJO_REPO" \
    --label "org.opencontainers.image.version=sha-$SOURCE_SHA" \
    --label "org.opencontainers.image.created=$created"
  forge-ci artifact upload --name "runtime-$target-arch-$arch" --dir "runtime-$target-arch-$arch/" --retention-days 1 --if-no-files error
  ;;
assemble)
  target=${2:?}
  image=$(runtime_publication plan --target "$target")
  repo=$(jq -er .repository <<<"$image")
  tag=$(jq -er .tag <<<"$image")
  archs=$(jq -er '.archs[]' <<<"$image")
  # Syft and Trivy use Docker's keychain, not the containers-auth variable.
  # Bridge the same isolated login without putting credentials in argv or
  # artifacts. The engine operations below still take the explicit auth file.
  DOCKER_CONFIG=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/runtime-registry.XXXXXX")
  export DOCKER_CONFIG
  trap 'rm -rf -- "$DOCKER_CONFIG"' EXIT
  install -m 0600 "${REGISTRY_AUTH_FILE:?}" "$DOCKER_CONFIG/config.json"
  download_metadata "$target" "$archs"
  image=$(runtime_publication plan --target "$target" --metadata-root "$PWD")
  # Check every architecture before scanning or publishing a combined index.
  while read -r arch; do
    ref=$(jq -er --arg arch "$arch" '.candidates[] | select(.arch == $arch) | .ref' <<<"$image")
    verify_smoke "$target" "$arch" "$ref"
  done <<<"$archs"
  mkdir -p evidence
  scan_status=0
  while read -r arch; do
    ref=$(jq -er --arg arch "$arch" '.candidates[] | select(.arch == $arch) | .ref' <<<"$image")
    verify_runtime_cli "$arch" "$ref"
    # Scan the native digest, not a tag or a default platform of an index.
    syft "registry:$ref" -o "cyclonedx-json=evidence/sbom-$arch.json"
    # The engine's policy, threshold and the approvals in .trivyignore.yaml.
    # The caller's enforce-vulnerabilities input decides whether a finding
    # blocks; false reports it. Scan every architecture before stopping, so
    # one run lists all blocking findings.
    forge-ci security scan container --enforce-vulnerabilities="${RUNTIME_IMAGES_ADMIN_VULNERABILITIES:?}" --image-ref "$ref" \
      --json-file "evidence/trivy-$arch.json" --sarif-file "evidence/trivy-$arch.sarif" \
      --gitlab-report-file "evidence/gl-container-scanning-$arch.json" || scan_status=$?
    cp -r "runtime-$target-arch-$arch" evidence/
  done <<<"$archs"
  if [ "$scan_status" -ne 0 ]; then
    # Nothing is assembled or signed; keep the reports of the refused scan.
    bash scripts/ci/runtime-evidence.sh "runtime-scan-$target" evidence/ ||
      printf 'runtime-images: the reports of the refused scan were not uploaded\n' >&2
    exit "$scan_status"
  fi
  forge-ci container multiarch-image assemble --auth-file "${REGISTRY_AUTH_FILE:?}" \
    --source-sha "$SOURCE_SHA" --server-url "${FORGEJO_SERVER:?}" --repository "${FORGEJO_REPO:?}" \
    --image-repository "$repo" --tag "$tag" --name "runtime-$target" --archs "$archs" \
    --manifest-output evidence/index.json
  cp "runtime-$target-dist/runtime-$target.json" evidence/image.json
  (cd evidence && sha256sum sbom-*.json >sboms.sha256)
  bash scripts/ci/runtime-evidence.sh "runtime-evidence-$target" evidence/
  ;;
*) fail 'expected plan, stage-cli, verify-cli, verify-smoke, build, collect or assemble' ;;
esac
