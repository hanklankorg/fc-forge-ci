#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
set -euo pipefail
root=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
mkdir -p "$work/repo/bin" "$work/repo/dist" "$work/outside"
printf 'bin := "./bin"\ndist := "./dist"\nRED := ""\nNC := ""\n' >"$work/repo/justfile"
sed -n '/^clean-build:/,/^$/p' "$root/justfile" >>"$work/repo/justfile"
printf keep >"$work/outside/canary"
status=0
just --justfile "$work/repo/justfile" --set bin "$work/outside" clean-build >"$work/log" 2>&1 || status=$?
[[ "$status" == 2 && -f "$work/outside/canary" && -d "$work/repo/bin" ]]
just --justfile "$work/repo/justfile" clean-build
[[ ! -e "$work/repo/bin" && ! -e "$work/repo/dist" ]]
ln -s "$work/outside" "$work/repo/bin"
status=0
just --justfile "$work/repo/justfile" clean-build >"$work/log" 2>&1 || status=$?
[[ "$status" == 1 && -f "$work/outside/canary" && -L "$work/repo/bin" ]]
printf 'Build cleanup boundaries passed.\n'
