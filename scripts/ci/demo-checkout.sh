#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Makes a built-in demo, test/live/fixtures/consumers/<name>/consumer, a repository of one
# commit, which a consumer trial reads like any clean consumer checkout. The
# destination must be an empty directory or not exist yet.
#
#   demo-checkout.sh <name> <destination>
set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd -P)

name=${1:-}
destination=${2:-}
if [[ $# -ne 2 || -z "$destination" ]]; then
  printf 'usage: %s <demo name> <destination>\n' "$0" >&2
  exit 2
fi
if [[ ! "$name" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
  printf 'demo name must be lowercase letters, digits and hyphens, got %q\n' "$name" >&2
  exit 2
fi
source_dir=$repo_root/test/live/fixtures/consumers/$name/consumer
if [[ ! -d "$source_dir" ]]; then
  printf 'no demo %q in test/live/fixtures/consumers\n' "$name" >&2
  exit 2
fi
if [[ -e "$destination" && -n "$(ls -A -- "$destination")" ]]; then
  printf 'the demo checkout %q exists and is not empty\n' "$destination" >&2
  exit 2
fi

mkdir -p -- "$destination"
cp -R -- "$source_dir/." "$destination/"
git -C "$destination" init --quiet --object-format=sha1 --initial-branch=main
git -C "$destination" add --all
git -C "$destination" -c user.name='Forge CI demo' -c user.email=demo@forge-ci.invalid \
  -c commit.gpgsign=false -c core.hooksPath=/dev/null commit --quiet -m "feat: add the $name demo"
