#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Runs the live-forge conformance tier: preflight the lab contract, check host
# tools, build the product, run the suite, keep a credential-redacted log.
#
#   run-live-tests.sh full
#   run-live-tests.sh focused <go-test-run-filter>
set -euo pipefail
umask 077

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd -P)
go_bin=${GO:-go}

usage() {
	printf 'usage: %s full\n       %s focused <go-test-run-filter>\n' "$0" "$0" >&2
	exit 2
}

profile=${1:-}
scenario=
case "$profile" in
full) [[ $# -eq 1 ]] || usage ;;
focused)
	[[ $# -eq 2 && -n "$2" ]] || usage
	scenario=$2
	;;
*) usage ;;
esac
export RC_LIVE_PROFILE="$profile"

# Preflight first, before anything is built: it locates the contract for this
# suite's lab account (LAB_TARGETS_FILE overrides the default path) and
# validates it exactly as the scenarios will. A refusal says where it looked;
# an accepted contract is explained, run identity included, so the operator
# sees what the run will touch. The report is kept for the log header.
preflight_report=$(cd -- "$repo_root" && "$go_bin" run ./internal/livetest/preflight)
printf '%s\n' "$preflight_report"

missing=()
for tool in base64 cosign flock jq syft buildah git sha256sum; do
	command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
((${#missing[@]} == 0)) || {
	printf 'live: required tools are not on PATH: %s\n' "${missing[*]}" >&2
	printf 'They are pinned in .mise.toml; install them with: mise install\n' >&2
	exit 2
}

# One run per lab account at a time: scenarios delete and recreate fixed
# rc-<scenario> repositories, so two concurrent runs would clobber each other.
# The lock lives at a fixed state path, not under RC_LIVE_EVIDENCE_ROOT, so
# moving the log cannot split it. It covers runs sharing this $HOME only; a
# run from a container with its own HOME does not see it. The shell holds
# fd 9 until it exits; the lock releases with it.
lock_dir=${XDG_STATE_HOME:-$HOME/.local/state}/forge-ci
install -d -m 0700 -- "$lock_dir"
exec 9>"$lock_dir/live.lock"
flock -n 9 || {
	printf 'live: another live run holds %s; wait for it to finish\n' "$lock_dir/live.lock" >&2
	exit 2
}

# Redacts credentials by shape, because the log is written before any
# scenario runs and cannot know the values: forge token prefixes, credential
# headers, URL userinfo, credential flags, and credential-named keys.
sanitize_live_log() {
	sed -u -E \
		-e 's/(gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,})/[REDACTED]/g' \
		-e 's/((Authorization|Proxy-Authorization|PRIVATE-TOKEN|JOB-TOKEN|X-Registry-Auth):[[:space:]]*((Basic|Bearer|token)[[:space:]]*)?)[^[:space:]"]+/\1[REDACTED]/Ig' \
		-e 's/((Set-)?Cookie:[[:space:]]*).*/\1[REDACTED]/Ig' \
		-e 's#(://)[^/@[:space:]]+@#\1[REDACTED]@#g' \
		-e 's/(--?(password|passwd|token|secret)[[:space:]]+)[^[:space:]]+/\1[REDACTED]/Ig' \
		-e "s/((\\bauth|token|password|passwd|secret|client_secret)[\"']?[[:space:]]*[:=][[:space:]]*[\"']?)[^,\"'[:space:]}]+/\\1[REDACTED]/Ig"
}

# The log survives the run under the XDG state directory (30-day retention),
# because the live tier is human-invoked and has no CI artifact to retrieve.
log_root=${RC_LIVE_EVIDENCE_ROOT:-${XDG_STATE_HOME:-$HOME/.local/state}/forge-ci/live-runs}
[[ "$log_root" == /* ]] || {
	printf 'RC_LIVE_EVIDENCE_ROOT must be absolute, got %q\n' "$log_root" >&2
	exit 2
}
install -d -m 0700 -- "$log_root"
find "$log_root" -mindepth 1 -maxdepth 1 -type d -name '20??????T??????Z-*' -mtime +30 -exec rm -rf -- {} +
run_dir="$log_root/$(date -u +%Y%m%dT%H%M%SZ)-$$"
install -d -m 0700 -- "$run_dir"
log="$run_dir/live-run.log"

state=$(mktemp -d "${TMPDIR:-/tmp}/forge-ci-live.XXXXXXXX")
trap 'rm -rf -- "$state"' EXIT

# The product is built here, outside the tests, and handed over by path: a
# suite that compiles its own binary proves something about the source it
# happened to see, not about the artifact the release flow produces.
(
	cd -- "$repo_root"
	CGO_ENABLED=0 "$go_bin" build -trimpath -buildvcs=false -o "$state/forge-ci-host" ./cmd/forge-ci
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$go_bin" build -trimpath -buildvcs=false \
		-o "$state/forge-ci-runner" ./cmd/forge-ci
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$go_bin" build -trimpath -buildvcs=false \
		-o "$state/credential-proxy" ./internal/livetest/credentialproxy
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$go_bin" build -trimpath -buildvcs=false \
		-o "$state/probe-json" ./internal/livetest/probejson
	# The Forgejo layer's binary, as its jobs download it: the one build the
	# tree's checksum names, which the layer scenarios publish to the lab.
	bash scripts/ci/forgejo-binary.sh build "$state/forge-ci-layer"
	chmod 500 "$state/forge-ci-host" "$state/forge-ci-runner" "$state/credential-proxy" "$state/probe-json"
	"$state/forge-ci-host" --version >/dev/null
	# Run the compiled suites directly: the go test driver's signal handling
	# can terminate a test before its cancellation-aware cleanup runs.
	install -d -m 0700 -- "$state/tests"
	"$go_bin" test -c -tags=live -buildvcs=false -o "$state/tests/" ./internal/livetest/... 9>&-
)
packages=$(cd -- "$repo_root" && "$go_bin" list -tags=live -f '{{if or .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}' ./internal/livetest/...)

export RC_LIVE_BIN="$state/forge-ci-host"
export RC_LIVE_RUNNER_BIN="$state/forge-ci-runner"
export RC_LIVE_RUNNER_SHA256
RC_LIVE_RUNNER_SHA256=$(sha256sum "$state/forge-ci-runner" | cut -d' ' -f1)
export RC_LIVE_CREDENTIAL_PROXY_BIN="$state/credential-proxy"
export RC_LIVE_PROBE_JSON_BIN="$state/probe-json"
export RC_LIVE_LAYER_BIN="$state/forge-ci-layer"

{
	printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	printf 'source_commit=%s\n' "$(git -C "$repo_root" rev-parse --verify HEAD 2>/dev/null || printf 'unavailable')"
	printf 'profile=%s\n' "$profile"
	printf 'scenario=%q\n' "$scenario"
	# What a consumer recipe was given, so the run can be repeated from its
	# log. A setting names a variable or a secret and never carries a secret.
	# An external registry's token stays in its file; its path and user are logged.
	for input in RC_LIVE_CONSUMER RC_LIVE_CONSUMER_EVENT RC_LIVE_CONSUMER_SETTINGS RC_LIVE_CONSUMER_VISIBILITY \
		RC_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE RC_LIVE_EXTERNAL_REGISTRY_USER; do
		[[ -z "${!input:-}" ]] || printf '%s=%q\n' "${input,,}" "${!input}"
	done
	printf '%s\n' "$preflight_report"
} >"$log"

filter=()
[[ -z "$scenario" ]] || filter=(-test.run "$scenario")

# Keep the logger alive during cancellation so cleanup failures reach the
# durable evidence. Neither it nor test children inherit the account lock.
exec 8> >(
	exec 9>&-
	trap '' INT TERM
	sanitize_live_log | tee -a "$log"
)
logger_pid=$!
suite_pid=
interrupted=0
trap 'interrupted=1; if [[ -n "$suite_pid" ]]; then kill -TERM "$suite_pid" 2>/dev/null || true; fi' INT TERM

# A trapped signal interrupts bash's wait before the child finishes cleanup.
wait_for_child() {
	local pid=$1 code
	while true; do
		wait "$pid"
		code=$?
		if ! kill -0 "$pid" 2>/dev/null; then return "$code"; fi
	done
}

# Packages remain serial under the existing account lock. Preserve each test
# package's working directory and the longest consumer-trial timeout.
set +e
status=0
while IFS= read -r package; do
	[[ -n "$package" ]] || continue
	[[ "$interrupted" -eq 0 ]] || break
	(
		cd -- "$package" || exit
		exec "$state/tests/${package##*/}.test" -test.paniconexit0 -test.count=1 -test.timeout=90m -test.v "${filter[@]}" 9>&- 8>&-
	) >&8 2>&1 &
	suite_pid=$!
	wait_for_child "$suite_pid"
	code=$?
	suite_pid=
	if [[ "$code" -ne 0 ]]; then status=$code; fi
	if [[ "$code" -eq 130 ]]; then interrupted=1; fi
done <<<"$packages"
exec 8>&-
wait_for_child "$logger_pid"
logger_status=$?
if [[ "$logger_status" -ne 0 && "$status" -eq 0 ]]; then status=$logger_status; fi
if [[ "$interrupted" -ne 0 ]]; then status=130; fi
set -e

# One verdict for the run: each test package above ends with a PASS or FAIL of
# its own, and a package holding none of the selected scenarios prints PASS.
case "$status" in
0) verdict=passed ;;
130) verdict=interrupted ;;
*) verdict=failed ;;
esac
printf 'Live result: %s\n' "$verdict"
printf 'Live log: %s (retained for 30 days)\n' "$log"
exit "$status"
