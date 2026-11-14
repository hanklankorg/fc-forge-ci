#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
# Live fixture: the native provider job's development build. Runs build.sh,
# builds and scans the images and writes development-artifacts/.
set -euo pipefail

# Run only in the native provider job. Registry destination and credentials come
# from that job's context; the controller supplies its disposable repository.
: "${SOURCE_SHA:?}" "${IMAGE_REF:?}" "${REGISTRY_USER:?}" "${REGISTRY_PASSWORD:?}"
test "$(git rev-parse HEAD)" = "$SOURCE_SHA"
export FIXTURE_VERSION="0.1.0-dev.${SOURCE_SHA:0:12}"
export SOURCE_DATE_EPOCH
SOURCE_DATE_EPOCH=$(git show -s --format=%ct HEAD)
bash ci/build.sh

auth_dir=$(mktemp -d)
auth="$auth_dir/config.json"
trap 'rm -rf -- "$auth_dir"' EXIT
printf '%s' "$REGISTRY_PASSWORD" | forge-ci container login --registry "${IMAGE_REF%%/*}" \
  --registry-username "$REGISTRY_USER" --registry-password-file - --auth-file "$auth"
mkdir development-artifacts .fixture-layouts
mkdir development-artifacts/linux-amd64
labels=$(printf 'org.opencontainers.image.revision=%s\norg.opencontainers.image.version=%s' "$SOURCE_SHA" "$FIXTURE_VERSION")

for role in worker api-service load-generator; do
  parent=".fixture-layouts/$role"
  layout="$parent/amd64"
  mkdir "$parent"
  forge-ci container build --mode oci --file Containerfile --context . \
    --target "$role" --platform linux/amd64 --output-dir "$layout" \
    --image-ref "$IMAGE_REF" --auth-file "$auth" --labels "$labels"
  forge-ci container layout record --layout-dir "$layout" \
    --image-ref "$IMAGE_REF" --source-sha "$SOURCE_SHA" --platform linux/amd64
  case "$role" in
  worker) paths='worker,usr/local/bin/key-tool' ;;
  api-service) paths='api-service' ;;
  load-generator) paths='usr/local/bin/load-generator' ;;
  esac
  forge-ci container layout extract-files --layout-dir "$layout" \
    --output-dir development-artifacts/linux-amd64 --paths "$paths"
  cp "$layout/forge-ci-image.json" "development-artifacts/$role-image.json"
done

# Publish only after every image has built, scanned and yielded its executables.
for role in worker api-service load-generator; do
  forge-ci container layout publish --layouts-dir ".fixture-layouts/$role" \
    --image-ref "$IMAGE_REF" --source-sha "$SOURCE_SHA" \
    --platforms linux/amd64 --auth-file "$auth"
done
forge-ci container suffix-extracted-binaries \
  --binaries-dir development-artifacts/linux-amd64 --arch amd64 \
  --expected-names worker,key-tool,api-service,load-generator
bash ci/receipt.sh
printf 'Rust development smoke passed at %s\n' "$SOURCE_SHA"
