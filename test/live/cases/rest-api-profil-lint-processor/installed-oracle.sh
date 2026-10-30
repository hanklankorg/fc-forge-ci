#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: CC0-1.0
set -euo pipefail
packages=$1
set -- "$packages"/*.tgz
test "$#" = 1
test -f "$1"
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
# The runner installs the retained package; there is no tar extraction and no
# application execution on the controller. Keep source/node_modules off PATH.
npm install --prefix "$scratch/prefix" --cache "$scratch/cache" --no-audit --no-fund "$1"
export PATH="$scratch/prefix/node_modules/.bin:/usr/local/bin:/usr/bin:/bin"
cd "$scratch"
raplp --version
raplp --help
cat > api.yaml <<'YAML'
openapi: 3.0.3
info:
  title: Installed package acceptance
  version: 1.0.0
paths: {}
YAML
# Assert actual execution, not merely the CLI's exit code (the app catches
# errors). Missing installed resources are application findings, never patched.
raplp --file "$scratch/api.yaml" --logDiagnostic "$scratch/diagnostics.json" > "$scratch/lint.log" 2>&1
cat "$scratch/lint.log"
if ! test -s "$scratch/diagnostics.json"; then
  printf '%s\n' 'APP_FINDING: installed raplp produced no diagnostic report; inspect packed API/document/ruleset resources' >&2
  exit 1
fi
printf '%s\n' 'INSTALLED_NPM_APPLICATION_OK'
