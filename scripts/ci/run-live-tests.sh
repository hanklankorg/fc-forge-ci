#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Runs the live-forge conformance tier: preflight the lab contract and host
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

print_help() {
  printf 'usage: %s full\n       %s focused <go-test-run-filter>\n' "$0" "$0"
  cat <<'EOF'

Runs the live-forge conformance tier against the Forge Lab: preflight the lab
contract and host tools, build the product, run the suite, keep a
credential-redacted log.

Profiles:
  full     every live scenario in the provider scope
  focused  only the scenarios <go-test-run-filter> matches (go test -run)

A table row of a prepared run that failed on a host its jobs could not reach,
and on nothing else, runs again once, after the run's other rows;
verdict.json has both attempts. docs/live-runs.md has the rule.

Environment:
  FORGE_CI_LIVE_PROVIDER  all (default), gitlab or forgejo
  FORGE_CI_LIVE_SET       full (default), medium or small: the table rows
                          of a prepared run, and the visibility of each,
                          that the run set has (docs/live-runs.md)
  FORGE_CI_LIVE_AFTER     small or medium, a set below FORGE_CI_LIVE_SET:
                          a delta run, of what FORGE_CI_LIVE_SET runs and
                          this set did not (docs/live-runs.md, Run sets)
  GO                      the go command (default: go)
  LAB_TARGETS_FILE        the lab contract (default:
                          $XDG_STATE_HOME/forge-lab/targets/forgeci.json)
  FORGE_CI_LIVE_EVIDENCE_ROOT
                          absolute directory for the run logs, kept 30 days
                          (default: $XDG_STATE_HOME/forge-ci/live-runs)
  FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR
                          full job logs (default: <run directory>/job-logs;
                          FORGE_CI_LIVE_SNAPSHOT_EVIDENCE is the old name);
                          a row's rerun keeps its own in rerun/ under it
  XDG_STATE_HOME          state root (default: ~/.local/state); the live
                          locks are forge-ci/live*.lock under it
  TMPDIR                  where the product is built for the run
  FORGE_CI_LIVE_DEMO, FORGE_CI_LIVE_CONSUMER*, FORGE_CI_LIVE_ADMIN_*,
  FORGE_CI_LIVE_EXTERNAL_REGISTRY_*, FORGE_CI_LIVE_*_IMAGE_BUILDER,
  FORGE_CI_LIVE_IMAGE_PLAN_JOB_IMAGE
                          scenario inputs; those set are recorded in the log
EOF
}

profile=${1:-}
scenario=
case "$profile" in
-h | --help)
  print_help
  exit 0
  ;;
full) [[ $# -eq 1 ]] || usage ;;
focused)
  [[ $# -eq 2 && -n "$2" ]] || usage
  scenario=$2
  ;;
-*)
  printf 'live: unknown option: %s\n' "$1" >&2
  usage
  ;;
*) usage ;;
esac
export FORGE_CI_LIVE_PROFILE="$profile"
provider_scope=${FORGE_CI_LIVE_PROVIDER:-all}
case "$provider_scope" in
all | gitlab | forgejo) ;;
*)
  printf 'live: FORGE_CI_LIVE_PROVIDER must be all, gitlab or forgejo\n' >&2
  exit 2
  ;;
esac
export FORGE_CI_LIVE_PROVIDER="$provider_scope"
# FORGE_CI_LIVE_PREFLIGHT=1 makes the run a preflight: each scenario runs its
# admission, every check it makes before an effect, and stops there; one that
# declares no admission boundary does not run. It writes nothing to the lab.
preflight=${FORGE_CI_LIVE_PREFLIGHT:-}
case "$preflight" in
'' | 1) ;;
*)
  printf 'live: FORGE_CI_LIVE_PREFLIGHT must be 1 or unset\n' >&2
  exit 2
  ;;
esac

# Preflight first, before anything is built: it locates the contract for this
# suite's lab account (LAB_TARGETS_FILE overrides the default path) and
# validates it exactly as the scenarios will. A refusal says where it looked;
# an accepted contract is explained, run identity included, so the operator
# sees what the run will touch. The report is kept for the log header.
preflight_report=$(cd -- "$repo_root" && "$go_bin" run ./test/live/internal/livetest/preflight)
printf '%s\n' "$preflight_report"

# Prepared images are of one commit. A run that takes them tests that commit
# and nothing on top of it: the binaries below are built from this tree. The
# header records the hash of the prepared.env it took, never its values, so
# the delta runs of one prepared directory can be told apart from others.
unset FORGE_CI_LIVE_PREPARED_ENV_SHA256
if [[ -n "${FORGE_CI_LIVE_PREPARED:-}" ]]; then
  (cd -- "$repo_root" && "$go_bin" run ./test/live/internal/livetest/preflight prepared-source \
    "$FORGE_CI_LIVE_PREPARED" \
    "$(git -C "$repo_root" rev-parse --verify HEAD)" \
    "$(git -C "$repo_root" status --porcelain --untracked-files=all)")
  FORGE_CI_LIVE_PREPARED_ENV_SHA256=$(sha256sum <"$FORGE_CI_LIVE_PREPARED/prepared.env" | cut -d' ' -f1)
  export FORGE_CI_LIVE_PREPARED_ENV_SHA256
fi

# A pattern of a prepared run normally selects a lane: many rows of the Go
# tables, which run one after another inside one test and take hours
# together. Go owns the tables and says which rows the pattern selects, each
# as the pattern that runs that row alone; the suite loop below runs a
# process for each. A run on a named consumer checkout has no rows, and a
# preflight stops every row at its admission.
#
# A run set below the full one (FORGE_CI_LIVE_SET) has fewer rows. The rows of
# the full set still say which tests are the tables', so a test whose rows
# are all outside the set is not run whole with everything else, and Go says
# what else the set runs ($others): the scenarios the pattern names exactly,
# and in the medium set the lanes' scenarios. Empty, nothing else runs.
# A delta run (FORGE_CI_LIVE_AFTER) has the rows and scenarios of its set
# that the set after did not run; of the full set, Go says which scenarios
# to skip ($skipped).
rows=()
table_rows=()
others=$scenario
skipped=
if [[ "$profile" == focused && -n "${FORGE_CI_LIVE_PREPARED:-}" && -z "${FORGE_CI_LIVE_CONSUMER:-}" && -z "$preflight" ]]; then
  # go run gives any refusal status 1; a pattern Go refuses is a usage error.
  focused_rows=$(cd -- "$repo_root" && "$go_bin" run ./test/live/internal/livetest/preflight focused-rows "$scenario") || exit 2
  [[ -z "$focused_rows" ]] || mapfile -t rows <<<"$focused_rows"
  table_rows=("${rows[@]}")
  if [[ "${FORGE_CI_LIVE_SET:-full}" != full || -n "${FORGE_CI_LIVE_AFTER:-}" ]]; then
    focused_rows=$(cd -- "$repo_root" && FORGE_CI_LIVE_SET=full FORGE_CI_LIVE_AFTER='' "$go_bin" run ./test/live/internal/livetest/preflight focused-rows "$scenario") || exit 2
    [[ -z "$focused_rows" ]] || mapfile -t table_rows <<<"$focused_rows"
    others=$(cd -- "$repo_root" && "$go_bin" run ./test/live/internal/livetest/preflight focused-scenarios "$scenario") || exit 2
    skipped=$(cd -- "$repo_root" && "$go_bin" run ./test/live/internal/livetest/preflight focused-skip "$scenario") || exit 2
  fi
fi

# The pinned host tools the scenarios run must resolve to real executables,
# not to a mise shim with nothing installed behind it; Go owns the list and
# the resolution the scenarios use.
(cd -- "$repo_root" && "$go_bin" run ./test/live/internal/livetest/preflight host-tools) >/dev/null

# An all-provider launcher owns live.lock exclusively. A provider-scoped run
# takes it shared and the lock of its lab account on that provider
# exclusively: a run's scratch repositories and packages all sit
# under its account, so independent providers may overlap, and so may runs on
# one provider under different accounts (each its own LAB_TARGETS_FILE), while
# two runs on one account cannot.
# Locks stay at fixed state paths and are never unlinked. The shell alone holds
# them through test cleanup; changing the evidence directory cannot split them.
# A preflight writes nothing to the lab, so it takes no lock and may run beside
# another run.
lock_dir=${XDG_STATE_HOME:-$HOME/.local/state}/forge-ci
install -d -m 0700 -- "$lock_dir"
if [[ -n "$preflight" ]]; then
  printf 'live: preflight: each scenario stops at its admission; nothing is written to the lab\n'
else
  exec 9>"$lock_dir/live.lock"
  lock_mode=-x
  [[ "$provider_scope" == all ]] || lock_mode=-s
  flock "$lock_mode" -n 9 || {
    printf 'live: another live run holds %s; wait for it to finish\n' "$lock_dir/live.lock" >&2
    exit 2
  }
  if [[ "$provider_scope" != all ]]; then
    account=$(cd -- "$repo_root" && "$go_bin" run ./test/live/internal/livetest/preflight provider-account 9>&-)
    [[ "$account" =~ ^[a-z0-9][a-z0-9-]*$ ]] || {
      printf 'live: the %s lab account %q cannot name a lock\n' "$provider_scope" "$account" >&2
      exit 2
    }
    account_lock=$lock_dir/live-$provider_scope-$account.lock
    exec 6>"$account_lock"
    flock -n 6 || {
      printf 'live: another %s live run as %s holds %s; wait for it to finish, or run as another lab account\n' "$provider_scope" "$account" "$account_lock" >&2
      exit 2
    }
  fi
fi

# Redacts credentials by shape, because the log is written before any
# scenario runs and cannot know the values: forge token prefixes, credential
# headers, URL userinfo, credential flags, and credential-named keys.
sanitize_live_log() {
  # Bash resets SIGINT when it forks pipeline commands. Install the ignore
  # in this child and exec the reader so cancellation cannot close the log.
  trap '' INT TERM
  LC_ALL=C exec sed -u -E \
    -e 's/\x1b\[[0-?]*[ -/]*[@-~]//g' \
    -e 's/(gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,})/[REDACTED]/g' \
    -e 's/((Authorization|Proxy-Authorization|PRIVATE-TOKEN|JOB-TOKEN|X-Registry-Auth):[[:space:]]*((Basic|Bearer|token)[[:space:]]*)?)[^[:space:]"]+/\1[REDACTED]/Ig' \
    -e 's/((Set-)?Cookie:[[:space:]]*).*/\1[REDACTED]/Ig' \
    -e 's#(://)[^/@[:space:]]+@#\1[REDACTED]@#g' \
    -e 's/(--?(password|passwd|token|secret)[[:space:]]+)[^[:space:]]+/\1[REDACTED]/Ig' \
    -e "s/((\\bauth|token|password|passwd|secret|client_secret)[\"']?[[:space:]]*[:=][[:space:]]*[\"']?)[^,\"'[:space:]}]+/\\1[REDACTED]/Ig"
}

# The log survives the run under the XDG state directory (30-day retention),
# because the live tier is human-invoked and has no CI artifact to retrieve.
log_root=${FORGE_CI_LIVE_EVIDENCE_ROOT:-${XDG_STATE_HOME:-$HOME/.local/state}/forge-ci/live-runs}
[[ "$log_root" == /* ]] || {
  printf 'FORGE_CI_LIVE_EVIDENCE_ROOT must be absolute, got %q\n' "$log_root" >&2
  exit 2
}
install -d -m 0700 -- "$log_root"
find "$log_root" -mindepth 1 -maxdepth 1 -type d -name '20??????T??????Z-*' -mtime +30 -exec rm -rf -- {} +
run_dir="$log_root/$(date -u +%Y%m%dT%H%M%SZ)-$$"
install -d -m 0700 -- "$run_dir"
log="$run_dir/live-run.log"
# Full redacted job logs must survive scratch-repository cleanup too. Honor
# both evidence overrides, including the legacy name, before the durable default.
export FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR="${FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR:-${FORGE_CI_LIVE_SNAPSHOT_EVIDENCE:-$run_dir/job-logs}}"

# The runner binaries are built for the architecture the lab's runners
# execute, as the contract states it; nothing here runs under emulation.
runner_arch=$(cd -- "$repo_root" && "$go_bin" run ./test/live/internal/livetest/preflight runner-arch)
printf 'live: building runner binaries for linux/%s, the lab runners architecture\n' "$runner_arch"

state=$(mktemp -d "${TMPDIR:-/tmp}/forge-ci-live.XXXXXXXX")
trap 'rm -rf -- "$state"' EXIT

# The product is built here, outside the tests, and handed over by path: a
# suite that compiles its own binary proves something about the source it
# happened to see, not about the artifact the release flow produces.
(
  cd -- "$repo_root"
  CGO_ENABLED=0 "$go_bin" build -trimpath -buildvcs=false -o "$state/forge-ci-host" ./cmd/forge-ci
  CGO_ENABLED=0 GOOS=linux GOARCH="$runner_arch" "$go_bin" build -trimpath -buildvcs=false \
    -o "$state/forge-ci-runner" ./cmd/forge-ci
  CGO_ENABLED=0 GOOS=linux GOARCH="$runner_arch" "$go_bin" build -trimpath -buildvcs=false \
    -o "$state/credential-proxy" ./test/live/internal/livetest/credentialproxy
  CGO_ENABLED=0 GOOS=linux GOARCH="$runner_arch" "$go_bin" build -trimpath -buildvcs=false \
    -o "$state/probe-json" ./test/live/internal/livetest/probejson
  # The Forgejo layer's binary, as its jobs download it: the build of the
  # runners' architecture that the tree's checksum names, which the layer
  # scenarios publish to the lab.
  bash scripts/ci/engine-binary.sh build "$runner_arch" "$state/forge-ci-layer"
  chmod 500 "$state/forge-ci-host" "$state/forge-ci-runner" "$state/credential-proxy" "$state/probe-json"
  "$state/forge-ci-host" --version >/dev/null
  # Run the compiled suites directly: the go test driver's signal handling
  # can terminate a test before its cancellation-aware cleanup runs.
  install -d -m 0700 -- "$state/tests"
  "$go_bin" test -c -tags=live -buildvcs=false -o "$state/tests/" ./test/live/internal/livetest/... ./test/live/conformance ./test/live/githubconsumer 6>&- 9>&-
) 6>&- 9>&-
packages=$(cd -- "$repo_root" && "$go_bin" list -tags=live -f '{{if or .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}' ./test/live/internal/livetest/... ./test/live/conformance ./test/live/githubconsumer 6>&- 9>&-)

export FORGE_CI_LIVE_BIN="$state/forge-ci-host"
export FORGE_CI_LIVE_RUNNER_BIN="$state/forge-ci-runner"
export FORGE_CI_LIVE_RUNNER_SHA256
FORGE_CI_LIVE_RUNNER_SHA256=$(sha256sum "$state/forge-ci-runner" | cut -d' ' -f1)
export FORGE_CI_LIVE_CREDENTIAL_PROXY_BIN="$state/credential-proxy"
export FORGE_CI_LIVE_PROBE_JSON_BIN="$state/probe-json"
export FORGE_CI_LIVE_LAYER_BIN="$state/forge-ci-layer"

started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
source_commit=$(git -C "$repo_root" rev-parse --verify HEAD 2>/dev/null || printf 'unavailable')
{
  printf 'started_at=%s\n' "$started_at"
  printf 'source_commit=%s\n' "$source_commit"
  printf 'profile=%s\n' "$profile"
  printf 'provider=%s\n' "$provider_scope"
  printf 'scenario=%q\n' "$scenario"
  [[ -z "$preflight" ]] || printf 'preflight=1\n'
  # What a consumer recipe was given, so the run can be repeated from its
  # log. A setting names a variable or a secret and never carries a secret.
  # An external registry's token stays in its file; its path and user are logged.
  # livetest.LiveRunInputs is this list for verdict.json; a test keeps the two equal.
  for input in FORGE_CI_LIVE_DEMO FORGE_CI_LIVE_CONSUMER FORGE_CI_LIVE_CONSUMER_EVENT FORGE_CI_LIVE_CONSUMER_SETTINGS FORGE_CI_LIVE_CONSUMER_VISIBILITY FORGE_CI_LIVE_IMAGE_PLAN_JOB_IMAGE \
    FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE FORGE_CI_LIVE_EXTERNAL_REGISTRY_USER FORGE_CI_LIVE_GITLAB_IMAGE_BUILDER FORGE_CI_LIVE_FORGEJO_IMAGE_BUILDER \
    FORGE_CI_LIVE_ADMIN_LINT FORGE_CI_LIVE_ADMIN_VULNERABILITIES FORGE_CI_LIVE_ADMIN_APPLICATION_TESTS FORGE_CI_LIVE_ADMIN_REPRODUCIBLE_BUILDS \
    FORGE_CI_LIVE_SET FORGE_CI_LIVE_AFTER FORGE_CI_LIVE_PREPARED_ENV_SHA256; do
    [[ -z "${!input:-}" ]] || printf '%s=%q\n' "${input,,}" "${!input}"
  done
  printf '%s\n' "$preflight_report"
} >"$log"

filter=()
[[ -z "$others" ]] || filter=(-test.run "$others")

# Keep the logger alive during cancellation so cleanup failures reach the
# durable evidence. Neither it nor test children inherit either live lock.
open_log() {
  exec 8> >(
    exec 6>&-
    exec 9>&-
    trap '' INT TERM
    sanitize_live_log | (
      trap '' INT TERM
      exec tee -a "$log"
    )
  )
  logger_pid=$!
}

# close_log ends the logger and waits for it, so the log on disk is whole.
logger_status=0
close_log() {
  local code
  exec 8>&-
  wait_for_child "$logger_pid"
  code=$?
  if [[ "$code" -ne 0 ]]; then logger_status=$code; fi
}

open_log
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

# run_suite <package> <row> <test flag>... runs a package's test binary once,
# in the package's working directory, between the package records the
# classifier reads a process by. A row's process ends with the row's line,
# for whoever watches the run: how the process ended, how long it took and
# the pattern that reruns the row. The classifier takes the line for the mark
# of a row's process, in which a case that ran no leaf is not covered.
#
# The binary's limit is the longest consumer-trial wait (150 minutes) plus
# room for the trial's setup and cleanup, so the trial's own deadline, not a
# panic, ends a slow run. That sizes it for one row, which is why a row gets
# a process of its own: a test binary that runs into the limit panics, and
# the panic loses the verdict of every subtest whose parent has not ended
# and skips their cleanup. Per row, that costs the one row.
#
# codes holds each process's exit status in the order they ran. A row's rerun
# ($rerun) is marked in the log, keeps its job logs in a directory of its
# own beside the first attempt's, and puts its status in the place of the
# first attempt's, so the run's status is of each row's last process.
codes=()
declare -A row_slot=()
rerun=
run_suite() {
  local package=$1 row=$2 code ended=passed began=$SECONDS
  shift 2
  printf 'FORGE_CI_LIVE_PACKAGE_BEGIN %s\n' "$package" >&8
  [[ -z "$rerun" ]] || printf 'FORGE_CI_LIVE_RERUN %s\n' "$row" >&8
  (
    cd -- "$package" || exit
    [[ -z "$rerun" ]] || export FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR="$FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR/rerun"
    exec "$state/tests/${package##*/}.test" -test.paniconexit0 -test.count=1 -test.timeout=3h -test.v "$@" 6>&- 9>&- 8>&-
  ) >&8 2>&1 &
  suite_pid=$!
  wait_for_child "$suite_pid"
  code=$?
  suite_pid=
  if [[ "$code" -eq 130 ]]; then ended=interrupted; elif [[ "$code" -ne 0 ]]; then ended=failed; fi
  [[ -z "$row" ]] || printf 'FORGE_CI_LIVE_ROW %s %dm%02ds %s\n' "$ended" "$(((SECONDS - began) / 60))" "$(((SECONDS - began) % 60))" "$row" >&8
  printf 'FORGE_CI_LIVE_PACKAGE_END %s\n' "$package" >&8
  if [[ -n "$rerun" ]]; then
    codes[${row_slot[$package$'\t'$row]}]=$code
  else
    [[ -z "$row" ]] || row_slot[$package$'\t'$row]=${#codes[@]}
    codes+=("$code")
  fi
  if [[ "$code" -eq 130 ]]; then interrupted=1; fi
}

# The tests whose rows run a process each, as one pattern: the binary that
# has such a test runs its rows, and the run of everything else the pattern
# selects leaves these tests out.
row_tests=
for row in "${table_rows[@]}"; do
  [[ "|$row_tests|" == *"|${row%%/*}|"* ]] || row_tests+=${row_tests:+|}${row%%/*}
done
rows_run=0

# Packages remain serial within the selected scope, and so do a package's
# rows: a failed, timed-out or crashed row does not stop the rows after it,
# and an interrupt stops after the row it reaches.
set +e
status=0
while IFS= read -r package; do
  [[ -n "$package" ]] || continue
  [[ "$interrupted" -eq 0 ]] || break
  rest=("${filter[@]}")
  skip=$skipped
  if [[ ${#rows[@]} -gt 0 ]]; then
    skip=$row_tests${skipped:+|$skipped}
    package_tests=$(cd -- "$package" && "$state/tests/${package##*/}.test" -test.list "$row_tests" 6>&- 9>&- 8>&-)
    for row in "${rows[@]}"; do
      [[ "$interrupted" -eq 0 ]] || break
      row_test=${row%%/*}
      [[ $'\n'"$package_tests"$'\n' == *$'\n'"${row_test:1:-1}"$'\n'* ]] || continue
      rows_run=$((rows_run + 1))
      run_suite "$package" "$row" -test.run "$row"
    done
    [[ "$interrupted" -eq 0 ]] || break
  fi
  [[ -z "$skip" ]] || rest+=(-test.skip "$skip")
  [[ -z "$scenario" || -n "$others" ]] || continue
  run_suite "$package" '' "${rest[@]}"
done <<<"$packages"

# A row that failed on a host its jobs could not reach, and on nothing else,
# runs again once, here: after everything else the run selected, so a resolver
# that stopped answering for a couple of minutes has had them, and inside
# this invocation and its locks. Go reads the rule from the log, which is
# whole once its logger has ended (livetest.RerunRows); only a row this
# invocation ran is run again, and a rerun never is. The rerun's process is
# the row's result, and the log and verdict.json keep both attempts.
if [[ "$interrupted" -eq 0 && ${#row_slot[@]} -gt 0 ]]; then
  close_log
  rerun_rows=$(cd -- "$repo_root" && "$go_bin" run ./test/live/internal/livetest/preflight rerun-rows "$log" 6>&- 9>&-) || rerun_rows=
  open_log
  rerun=1
  while IFS=$'\t' read -r package row cause; do
    [[ -n "$row" && -n "${row_slot[$package$'\t'$row]:-}" ]] || continue
    [[ "$interrupted" -eq 0 ]] || break
    printf 'Live cause: %s: %s; the row failed and runs again once\n' "$row" "$cause" >&8
    run_suite "$package" "$row" -test.run "$row"
  done <<<"$rerun_rows"
fi
for code in "${codes[@]}"; do
  if [[ "$code" -ne 0 ]]; then status=$code; fi
done
if [[ "$interrupted" -eq 0 && "$rows_run" -ne ${#rows[@]} ]]; then
  printf 'live: %d of %d selected rows ran: no test binary has the test of the others\n' "$rows_run" "${#rows[@]}" >&8
  status=1
fi
close_log
if [[ "$logger_status" -ne 0 && "$status" -eq 0 ]]; then status=$logger_status; fi
if [[ "$interrupted" -ne 0 ]]; then status=130; fi
set -e

# Result interpretation belongs to the Go test harness, including all-skipped
# trees, lab failures and log-read failures; shell retains build/process
# supervision only. The classifier also writes verdict.json beside the log,
# read from the redacted log and given the run's header as values, not as the
# shell-quoted text the log holds.
classification=$(cd -- "$repo_root" && FORGE_CI_LIVE_RUN_STARTED_AT="$started_at" FORGE_CI_LIVE_RUN_SOURCE_COMMIT="$source_commit" \
  FORGE_CI_LIVE_RUN_SCENARIO="$scenario" "$go_bin" run ./test/live/internal/livetest/preflight classify-run "$status" "$log" 6>&- 9>&-) || classification='1 failed'
read -r status verdict <<<"$classification"
if [[ "$interrupted" -ne 0 ]]; then
  status=130
  verdict=interrupted
fi
printf 'Live result: %s\n' "$verdict"
printf 'Live log: %s (retained for 30 days)\n' "$log"
[[ ! -f "$run_dir/verdict.json" ]] || printf 'Live verdict: %s\n' "$run_dir/verdict.json"
exit "$status"
