#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: CC0-1.0
set -euo pipefail
# Acceptance policy: the full Nanolinter plan is diagnostic. Application tests,
# setup, compiler checks and the non-Nanolinter parts of lint remain blocking.
kind=$(cat .gitlab/generic-cli-kind)
case "${kind}" in
go)
	export GOTOOLCHAIN=local
	test "$(go env GOVERSION)" = go1.27.3
	just _lint-linux-platform
	just --fmt --check
	go mod tidy -diff
	nano=nanolinter
	"${nano}" check-tools
	;;
cargo)
	test "$(rustc --version | cut -d ' ' -f2)" = 1.99.1
	just _build-verify-binary check-tools
	just --fmt --check
	nano=./target/release/nanolinter
	;;
*) exit 1 ;;
esac
# Refuse an old/excluded-check overlay rather than mislabel its coverage.
jq -e '.lintPolicy == "informational" and .excludedChecks == []' .gitlab/generic-cli-source.json >/dev/null
printf '%s\n' 'ACCEPTANCE POLICY: full Nanolinter plan runs informationally; prior findings (including 64 KICS findings) remain unresolved. This is not a full original just verify pass.'
lint_status=0
"${nano}" verify || lint_status=$?
printf 'NANOLINTER_DIAGNOSTIC exit=%s policy=informational excludedChecks=[]; full output/summary above retained in job log\n' "${lint_status}"
# 1 is Nanolinter's completed negative/operational verdict. Missing commands,
# config/usage errors, crashes and cancellation are invocation failures, not
# diagnostic findings. Tool readiness was independently required above.
case "${lint_status}" in 0 | 1) ;; *) exit "${lint_status}" ;; esac
exec just test
