#!/kaniko/tools/bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
# Entrypoint of the disposable GitHub Docker action that runs a kaniko image,
# extract or snapshot build through the CLI; embedded by githubaction.go.
set -euo pipefail
export PATH="/kaniko/tools:$PATH"
export CI_IMAGE_BUILDER=kaniko
unset FORGE_CI_PLAN
cd "$GITHUB_WORKSPACE/source"
# Trust only the host checkout, without changing persistent Git configuration.
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0="$PWD"
output="$GITHUB_WORKSPACE/.forge-ci-kaniko-output"
if [[ "${1:-image}" == snapshot ]]; then output="$PWD/snapshot-layouts"; fi
test ! -e "$output"

auth_file=
if [[ "${1:-image}" != snapshot && -n "${REGISTRY_PASSWORD:-}" ]]; then
  auth_file=/kaniko/.docker/config.json
  printf '%s' "$REGISTRY_PASSWORD" | forge-ci container login --registry "$CONTAINER_REGISTRY" \
    --registry-username "$REGISTRY_USER" --registry-password-file - --auth-file "$auth_file"
fi
unset REGISTRY_PASSWORD REGISTRY_TOKEN GH_TOKEN GITHUB_TOKEN ACTIONS_RUNTIME_TOKEN ACTIONS_RESULTS_URL

case "${1:-image}" in
image | extract)
  if [[ "${1:-image}" == image ]]; then
    export FORGE_CI_PLAN=/kaniko/tools/plan.json
  else
    unset FORGE_CI_PLAN
  fi
  # The calling step always sets the administrator's setting. Without it the
  # build stops: an unset value must not mean "do not enforce".
  exec forge-ci container build --image-builder kaniko --auth-file "$auth_file" \
    --output-dir "$output" \
    --enforce-vulnerabilities="${ADMIN_VULNERABILITIES:?the step must pass the enforce-vulnerabilities setting}"
  ;;
snapshot)
  unset FORGE_CI_PLAN
  exec forge-ci container snapshot-images build --image-builder kaniko \
    --plan /kaniko/tools/plan.json --dependencies-file snapshot-dependencies.json \
    --name "$IMAGE_NAME" --arch "$IMAGE_ARCH" --metadata-dir snapshot-layouts \
    --server-url "$GITHUB_SERVER_URL" --repository "$GITHUB_REPOSITORY"
  ;;
*) exit 64 ;;
esac
