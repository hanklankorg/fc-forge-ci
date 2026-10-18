#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Builds the signer image of this commit from containers/signer/Containerfile
# and publishes it to the lab registry with Forge Lab's image command, for the
# release scenarios of the live tier. Prints the digest-pinned reference, and
# only that, on standard output.
#
#   publish-live-signer-image.sh <forge-lab checkout>
set -euo pipefail
umask 077

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd -P)

forge_lab=${1:-}
if [[ $# -ne 1 || "$forge_lab" != /* || ! -f "$forge_lab/justfile" ]]; then
	printf 'usage: %s <absolute path of a Forge Lab checkout>\n' "$0" >&2
	exit 2
fi

missing=()
for tool in podman git "${GO:-go}" just; do
	command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
((${#missing[@]} == 0)) || {
	printf 'live: required tools are not on PATH: %s\n' "${missing[*]}" >&2
	exit 2
}

# The image is for the lab's runners, whose architecture the contract states,
# and the build executes the image's toolchain check. Ask the engine that
# builds it (on macOS a VM, not this controller) for its architecture, so the
# build is native rather than emulated.
runner_arch=$(cd -- "$repo_root" && "${GO:-go}" run ./internal/livetest/preflight runner-arch) || exit 2
engine_arch=$(podman info --format '{{.Host.Arch}}') || {
	printf 'live: cannot ask podman for its host architecture\n' >&2
	exit 2
}
if [[ "$engine_arch" != "$runner_arch" ]]; then
	printf 'live: the lab runners are linux/%s and the signer build runs the image; use a podman engine on a native %s host (this engine is %s)\n' "$runner_arch" "$runner_arch" "$engine_arch" >&2
	exit 2
fi

# The image is keyed by the commit, so what it is built from must be that
# commit's: the Containerfile, its ignore file and the one script it copies.
image_inputs=(containers/signer .forgejo/scripts/verify-signer-toolchain.sh)
if [[ -n "$(git -C "$repo_root" status --porcelain --untracked-files=all -- "${image_inputs[@]}")" ]]; then
	printf 'live: the signer image inputs have uncommitted changes; commit or stash them first:\n' >&2
	git -C "$repo_root" status --short --untracked-files=all -- "${image_inputs[@]}" >&2
	exit 2
fi
commit=$(git -C "$repo_root" rev-parse --verify HEAD)

# The Containerfile ends by running
# verify-signer-toolchain.sh in strict mode, so an image that builds has the
# reviewed toolchain and nothing the signing boundary forbids.
image="localhost/forge-ci-live-signer:$commit"
if podman image exists "$image"; then
	printf 'live: reusing the signer image built for %s\n' "$commit" >&2
else
	podman build --platform "linux/$runner_arch" \
		--file "$repo_root/containers/signer/Containerfile" \
		--label "org.opencontainers.image.revision=$commit" \
		--tag "$image" "$repo_root" >&2
fi

# Forge Lab publishes under the account's own owner, on the road and registry
# of the account's contract (LAB_TARGETS_FILE when set), and prints the digest
# reference as its one line of standard output. The package name is outside
# the fci- prefix, so a prefix reset on the lab does not delete the image.
just --justfile "$forge_lab/justfile" --working-directory "$forge_lab" \
	publish-image --account forgeci forge-ci-live-signer "$image"
