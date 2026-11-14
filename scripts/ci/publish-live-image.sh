#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Builds a job image of this commit and publishes it to the lab registry with
# Forge Lab's image command, for the live tier. Prints the digest-pinned
# reference, and only that, on standard output.
#
#   signer       containers/signer/Containerfile, for the release scenarios'
#                signer jobs.
#   kaniko-job   containers/kaniko-job/Containerfile, for a consumer trial
#                with CI_IMAGE_BUILDER=kaniko run on a named checkout. A run
#                admits it only when its source label names the repository
#                the layer runs from, so it is labelled with the contract
#                account's Forgejo consumer trial scratch layer. A prepared
#                case pushes its own from the saved archive instead.
#   buildah-job  containers/buildah-job/Containerfile, the GitLab release
#                policy's builder-job-image with image-builder: buildah.
#
#   publish-live-image.sh <signer|kaniko-job|buildah-job> <forge-lab checkout> [<archive>]
#
# With an archive path, the image published is also saved there as a
# docker-archive, for a GitLab trial that takes the image by archive.
set -euo pipefail
umask 077

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd -P)

kind=${1:-}
forge_lab=${2:-}
archive=${3:-}
if [[ $# -lt 2 || $# -gt 3 || ! "$kind" =~ ^(signer|kaniko-job|buildah-job)$ || "$forge_lab" != /* || ! -f "$forge_lab/justfile" ||
  ($# -eq 3 && "$archive" != /*) ]]; then
  printf 'usage: %s <signer|kaniko-job|buildah-job> <absolute path of a Forge Lab checkout> [<absolute archive path>]\n' "$0" >&2
  exit 2
fi

# shellcheck source=scripts/ci/image-build-settings.sh
source "$script_dir/image-build-settings.sh"
cd -- "$repo_root"
declare -a image_build_args
image_build_settings image_build_args image-build-settings '' '' '' '' || exit 2

missing=()
for tool in podman git "${GO:-go}" just; do
  command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
((${#missing[@]} == 0)) || {
  printf 'live: required tools are not on PATH: %s\n' "${missing[*]}" >&2
  exit 2
}

# What each image is built from. The image is keyed by the commit, so these
# must be that commit's: the signer's Containerfile, its ignore file and the
# one script it copies; a builder job image's own directory, its context.
source_url=
case "$kind" in
signer)
  context=$repo_root
  image_inputs=(containers/signer .forgejo/scripts/verify-signer-toolchain.sh)
  ;;
kaniko-job)
  context=$repo_root/containers/kaniko-job
  image_inputs=(containers/kaniko-job)
  source_url=$(cd -- "$repo_root" && "${GO:-go}" run ./test/live/internal/livetest/preflight consumer-trial-layer) || exit 2
  ;;
buildah-job)
  context=$repo_root/containers/buildah-job
  image_inputs=(containers/buildah-job)
  ;;
esac

# The image is for the lab's runners, whose architecture the contract states,
# and the build executes the image's own steps. Ask the engine that builds it
# (on macOS a VM, not this controller) for its architecture, so the build is
# native rather than emulated.
runner_arch=$(cd -- "$repo_root" && "${GO:-go}" run ./test/live/internal/livetest/preflight runner-arch) || exit 2
engine_arch=$(podman info --format '{{.Host.Arch}}') || {
  printf 'live: cannot ask podman for its host architecture\n' >&2
  exit 2
}
if [[ "$engine_arch" != "$runner_arch" ]]; then
  printf 'live: the lab runners are linux/%s and the %s build runs the image; use a podman engine on a native %s host (this engine is %s)\n' "$runner_arch" "$kind" "$runner_arch" "$engine_arch" >&2
  exit 2
fi

if [[ -n "$(git -C "$repo_root" status --porcelain --untracked-files=all -- "${image_inputs[@]}")" ]]; then
  printf 'live: the %s image inputs have uncommitted changes; commit or stash them first:\n' "$kind" >&2
  git -C "$repo_root" status --short --untracked-files=all -- "${image_inputs[@]}" >&2
  exit 2
fi
commit=$(git -C "$repo_root" rev-parse --verify HEAD)

# The signer's Containerfile ends by running verify-signer-toolchain.sh in
# strict mode, so an image that builds has the reviewed toolchain and nothing
# the signing boundary forbids. A local build is reused only when it names the
# same source, so a kaniko job image is never published for another lab.
image="localhost/forge-ci-live-$kind:$commit"
labels=(--label "org.opencontainers.image.revision=$commit")
[[ -z "$source_url" ]] || labels+=(--label "org.opencontainers.image.source=$source_url")
if podman image exists "$image" && [[ -z "$source_url" ||
  "$(podman image inspect --format '{{index .Labels "org.opencontainers.image.source"}}' "$image")" == "$source_url" ]]; then
  printf 'live: reusing the %s image built for %s\n' "$kind" "$commit" >&2
else
  podman build "${image_build_args[@]}" --platform "linux/$runner_arch" \
    --file "$repo_root/containers/$kind/Containerfile" \
    "${labels[@]}" \
    --tag "$image" "$context" >&2
fi

# Forge Lab publishes under the account's own owner, on the road and registry
# of the account's contract (LAB_TARGETS_FILE when set), and prints the digest
# reference as its one line of standard output. The package name is outside
# the fci- prefix, so a prefix reset on the lab does not delete the image.
just --justfile "$forge_lab/justfile" --working-directory "$forge_lab" \
  publish-image --account forgeci "forge-ci-live-$kind" "$image"
if [[ -n "$archive" ]]; then
  rm -f -- "$archive"
  podman save --quiet --format docker-archive --output "$archive" "$image"
fi
