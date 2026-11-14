#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Runs the hosted GitHub cells of a run set on the demos of a prepared
# directory: the engine is prepared once, then the cells run `just
# test-live-consumer github` with the prepared engine reused, up to
# max_cells at once and never two of one demo, as they share package
# coordinates. Within a cell the trial runs its two visibilities side by
# side, each in its own repository, unless the release publishes a package
# the organization names apart from the repository
# (livetest.GitHubReleaseSharesPackages); the log of a cell holds both,
# each line under the subtest Go names before it.
#
#   run-github-cells.sh <prepared-dir> <org-id> <set> [after=<set>] [setting ...]
#
# after=small or after=medium makes it a delta run: the cells and
# visibilities of the set that the set after did not run.
#
# Go owns the cells and their settings (livetest.GitHubCells) and refuses,
# before anything runs, a setting a cell does not take, a lint flavor with no
# nanolinter-image=<flavor>:<reference>, and a release with no
# FORGE_CI_LIVE_GITHUB_RELEASE_TOKEN_FILE. This script reads no credential.
# It starts no hosted session: run it inside Forge Lab's guarded one, as a
# single trial is. A cell is not run again.
#
# The run directory is one of the live launcher's (FORGE_CI_LIVE_EVIDENCE_ROOT,
# default $XDG_STATE_HOME/forge-ci/live-runs): a log for the engine and for
# each cell, numbered in the run set's order, the exit status of each cell's
# trial beside its log, and cells.txt with the run set, the set after of a
# delta run, and each cell's line as it ends.
set -euo pipefail
umask 077

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd -P)
go_bin=${GO:-go}
just_bin=${JUST:-just}

if [[ $# -lt 3 || "$1" != /* || ! -d "$1/demos" || -z "$2" ]]; then
  printf 'usage: %s <absolute prepared directory> <organization id> <set=small|medium|full> [after=small|medium] [setting ...]\n' "$0" >&2
  exit 2
fi
# wait -n -p, which tells which cell ended, is bash 5.1's.
if ((BASH_VERSINFO[0] * 100 + BASH_VERSINFO[1] < 501)); then
  printf 'run-github-cells.sh needs bash 5.1 or later, got %s\n' "$BASH_VERSION" >&2
  exit 2
fi
prepared=$1 org=$2 set=${3#set=} after=
shift 3
if [[ "${1:-}" == after=* ]]; then
  after=${1#after=}
  shift
fi

# go run gives any refusal status 1; a set or a setting Go refuses is a usage error.
cells=$(cd -- "$repo_root" && FORGE_CI_LIVE_AFTER="$after" "$go_bin" run ./test/live/internal/livetest/preflight github-cells "$set" "$@") || exit 2
[[ -n "$cells" ]] || {
  printf 'live: the %s set%s has no hosted GitHub cell\n' "$set" "${after:+ after $after}" >&2
  exit 2
}

log_root=${FORGE_CI_LIVE_EVIDENCE_ROOT:-${XDG_STATE_HOME:-$HOME/.local/state}/forge-ci/live-runs}
[[ "$log_root" == /* ]] || {
  printf 'FORGE_CI_LIVE_EVIDENCE_ROOT must be absolute, got %q\n' "$log_root" >&2
  exit 2
}
run_dir="$log_root/$(date -u +%Y%m%dT%H%M%SZ)-$$"
install -d -m 0700 -- "$run_dir"
{
  printf 'forge_ci_live_set=%s\n' "${set:-full}"
  [[ -z "$after" ]] || printf 'forge_ci_live_after=%s\n' "$after"
} >"$run_dir/cells.txt"

# trial <engine mode> <visibility> <log> <demo> <event> [setting ...] runs one
# call of the consumer recipe into its log. The visibility is the cell's:
# "both" runs with FORGE_CI_LIVE_CONSUMER_VISIBILITY unset, whatever the
# caller's environment has. A cell's trial runs in the background, where
# bash ignores SIGINT and SIGQUIT; it takes them back, so that an interrupt
# reaches it as it would in the foreground.
trial() {
  local mode=$1 visibility=$2 log=$3 demo=$4 event=$5
  shift 5
  (
    trap - INT QUIT
    export FORGE_CI_LIVE_ENGINE_MODE="$mode"
    unset FORGE_CI_LIVE_CONSUMER_VISIBILITY
    [[ "$visibility" == both ]] || export FORGE_CI_LIVE_CONSUMER_VISIBILITY="$visibility"
    exec "$just_bin" --justfile "$repo_root/justfile" test-live-consumer github "$prepared/demos/$demo" "$event" "org=$org" "$@"
  ) </dev/null >"$log" 2>&1
}

# An interrupt reaches the running trials too, which clean up after
# themselves; no further cell starts, and the run waits for those running.
interrupted=0
trap 'interrupted=1' INT TERM

# The engine is prepared once, with the first cell's consumer, which does
# not run: every cell then checks that engine and builds nothing.
IFS=$'\t' read -r visibility demo event words <<<"${cells%%$'\n'*}"
read -ra settings <<<"$words"
printf 'live: preparing the engine; log %s\n' "$run_dir/engine-prepare.log"
if ! trial prepare "$visibility" "$run_dir/engine-prepare.log" "$demo" "$event" "${settings[@]}" || [[ "$interrupted" -ne 0 ]]; then
  printf 'live: the engine was not prepared; no cell ran\n' >&2
  exit 1
fi

# At most max_cells cells run at once. A cell's two visibilities each run a
# workflow of mostly one to three jobs at a time, and the free organization
# runs 20 jobs at once on standard hosted runners: three cells stay roughly
# within it, and a job over it waits in the queue, within the run's
# 90-minute bound. Each visibility reads its run's status once every 20
# seconds, 180 requests an hour: the six of three cells take some 1,100 of
# the 5,000 an hour the session's token has.
readonly max_cells=3

# A cell passed when its trial exited clean and its test passed. A trial
# that exits clean with its test skipped, as every visibility not covered,
# ran nothing to judge; one that wrote no exit status failed. Only this
# shell writes a cell's line, once its trial has ended, so lines of cells
# that end together cannot interleave.
report() {
  local cell=$1 exit_status=1 verdict=failed
  [[ ! -s "${logs[cell]%.log}.status" ]] || read -r exit_status <"${logs[cell]%.log}.status"
  if [[ "$exit_status" -eq 0 ]]; then
    verdict=not-covered
    if grep -q '^--- PASS: TestGitHubLayer_Consumer' -- "${logs[cell]}"; then verdict=passed; fi
  fi
  [[ "$verdict" == passed ]] || status=1
  printf 'FORGE_CI_LIVE_CELL %s %s %s %s %s %s\n' "$verdict" "${demos[cell]}" "${events[cell]}" "${visibilities[cell]}" "${words[cell]:--}" "${logs[cell]}" | tee -a "$run_dir/cells.txt"
}

visibilities=() demos=() events=() words=() logs=() pending=()
while IFS=$'\t' read -r visibility demo event setting_words; do
  pending+=("${#demos[@]}")
  visibilities+=("$visibility") demos+=("$demo") events+=("$event") words+=("$setting_words")
  logs+=("$(printf '%s/%02d-%s-%s.log' "$run_dir" "${#demos[@]}" "$demo" "$event")")
done <<<"$cells"

# Each round starts, in the run set's order, every waiting cell there is room
# for whose demo has no cell running, then waits for one to end. A signal
# ends the wait early, with no cell named. Each cell writes its trial's exit
# status itself: a trapped signal that ends the wait as it reaps a cell
# loses that status in bash, which later names the cell with status 0.
status=0 ran=0
declare -A running=() busy=()
while :; do
  waiting=()
  for cell in "${pending[@]}"; do
    if [[ "$interrupted" -eq 0 && ${#running[@]} -lt $max_cells && -z "${busy[${demos[cell]}]:-}" ]]; then
      read -ra settings <<<"${words[cell]}"
      {
        trial_status=0
        trial reuse "${visibilities[cell]}" "${logs[cell]}" "${demos[cell]}" "${events[cell]}" "${settings[@]}" || trial_status=$?
        printf '%d\n' "$trial_status" >"${logs[cell]%.log}.status"
      } &
      running[$!]=$cell busy[${demos[cell]}]=1
    else
      waiting+=("$cell")
    fi
  done
  pending=("${waiting[@]}")
  [[ ${#running[@]} -gt 0 ]] || break
  exit_status=0
  wait -n -p ended -- "${!running[@]}" || exit_status=$?
  if [[ -z "${ended:-}" ]]; then
    # Only a signal ends the wait without naming a job; anything else would
    # wait on nothing again and again.
    [[ "$exit_status" -gt 128 ]] || { printf 'live: wait ended with status %d and no cell\n' "$exit_status" >&2; exit 1; }
    continue
  fi
  cell=${running[$ended]}
  unset "running[$ended]" "busy[${demos[cell]}]"
  ran=$((ran + 1))
  report "$cell"
done

if [[ "$interrupted" -ne 0 ]]; then
  printf 'live: interrupted after %d cell(s)\n' "$ran" >&2
  exit 130
fi
printf 'Live cells: %s\n' "$run_dir/cells.txt"
exit "$status"
