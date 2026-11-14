#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Lend this machine to the orchestrating forge for exactly one job.
#
# Installs the pinned forgejo-runner, registers it as an ephemeral runner
# with the labels the waiting job requires, and runs it until that one job
# is done or nobody has claimed it within the TTL. Every lender (a GitHub
# Actions VM, a GitLab CI job, a cloud VM's cloud-init) runs this same script;
# only how the machine came to exist differs.
#
# The runner runs as a dedicated unprivileged user with a clean environment:
# the job it hosts must not see the credentials of whatever started this
# machine, and rootless Buildah and Podman need an ordinary user with subordinate
# ids, which useradd supplies. Root is needed once, to create that user.
#
# Environment:
#   FORGEJO_INSTANCE_URL              https URL of the forge the runner registers with
#   FORGEJO_RUNNER_REGISTRATION_TOKEN the forge's runner registration token
#   RUNNER_LABELS                     comma-separated labels, e.g. gh-vm,arch=amd64;
#                                     the waiting job's CI_RUNNER_LABEL_CONTAINER names the same
#   LEND_TTL_MINUTES                  how long to wait for the job, 2..180
#   RUNNER_NAME                       optional; default lender-<hostname>-<pid>
#
# Usage: sudo --preserve-env=FORGEJO_INSTANCE_URL,FORGEJO_RUNNER_REGISTRATION_TOKEN,RUNNER_LABELS,LEND_TTL_MINUTES \
#          bash scripts/runner-lender/lend-forgejo-runner.sh

set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
readonly LENDER_USER=lender
readonly LENDER_HOME=/home/lender
readonly RUNNER_DIR="${LENDER_HOME}/runner"

fail() {
  printf 'lend-forgejo-runner: %s\n' "$1" >&2
  exit 1
}

# Runner 13.2.1 deletes an ephemeral registration after completing a job, not
# when its idle poller is interrupted. It has no unregister command or runner
# RPC. Do not report a successful retirement that needs forge-side authority.
idle_expired() {
  fail "No job claimed runner $1 within $2 minutes; worker stopped, but Forgejo registration retirement is unconfirmed. An authorized forge operator must verify and remove that exact registration."
}

# validate_lend_environment checks every input before anything touches the
# machine. Labels are restricted to the characters a Forgejo label may hold,
# so a caller cannot smuggle a schema (`:docker://…`) or shell syntax into
# the registration.
validate_lend_environment() {
  case "${FORGEJO_INSTANCE_URL:-}" in
  https://*) ;;
  *) fail 'FORGEJO_INSTANCE_URL must be an https:// URL' ;;
  esac
  [[ -n "${FORGEJO_RUNNER_REGISTRATION_TOKEN:-}" ]] || fail 'FORGEJO_RUNNER_REGISTRATION_TOKEN is empty'
  [[ "${RUNNER_LABELS:-}" =~ ^[A-Za-z0-9_.=-]+(,[A-Za-z0-9_.=-]+)*$ ]] || fail 'RUNNER_LABELS must be comma-separated labels of [A-Za-z0-9_.=-]'
  if ! [[ "${LEND_TTL_MINUTES:-}" =~ ^[0-9]+$ ]] || ((LEND_TTL_MINUTES < 2 || LEND_TTL_MINUTES > 180)); then
    fail 'LEND_TTL_MINUTES must be 2..180'
  fi
  [[ "${RUNNER_NAME:-x}" =~ ^[A-Za-z0-9_.-]+$ ]] || fail 'RUNNER_NAME must be [A-Za-z0-9_.-]'
}

# host_labels gives every label the host schema: the job's steps run directly
# on this machine, as the lender's user, where user namespaces are available.
host_labels() {
  local label out=""
  local -a labels
  IFS=, read -r -a labels <<<"$1"
  for label in "${labels[@]}"; do
    out+="${out:+,}${label}:host"
  done
  printf '%s' "$out"
}

# ensure_lender_user creates the unprivileged user the runner and its job run
# as. useradd assigns subordinate uid/gid ranges, which rootless containers need.
ensure_lender_user() {
  if ! id -u "$LENDER_USER" >/dev/null 2>&1; then
    useradd --create-home --home-dir "$LENDER_HOME" --shell /bin/bash "$LENDER_USER"
  fi
  grep -q "^${LENDER_USER}:" /etc/subuid || fail "no subordinate uid range for ${LENDER_USER}; rootless containers need one"
  install -d -m 0700 -o "$LENDER_USER" -g "$LENDER_USER" "$RUNNER_DIR" "${LENDER_HOME}/.runtime"
}

# as_lender runs a command as the lender user with a clean environment. Only
# PATH, HOME, USER and the runtime directory survive; the credentials of the
# process that started this machine do not. exec_as_lender replaces the
# calling shell with it, so a signal to that shell's pid reaches the runner.
as_lender() {
  sudo -u "$LENDER_USER" env -i \
    PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME="$LENDER_HOME" \
    USER="$LENDER_USER" \
    LOGNAME="$LENDER_USER" \
    XDG_RUNTIME_DIR="${LENDER_HOME}/.runtime" \
    "$@"
}

exec_as_lender() {
  exec sudo -u "$LENDER_USER" env -i \
    PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME="$LENDER_HOME" \
    USER="$LENDER_USER" \
    LOGNAME="$LENDER_USER" \
    XDG_RUNTIME_DIR="${LENDER_HOME}/.runtime" \
    "$@"
}

main() {
  validate_lend_environment
  [[ $(id -u) -eq 0 ]] || fail 'run as root: the lender user is created once, the runner then runs as that user'

  # shellcheck source=../bootstrap/install-forgejo-runner.sh
  source "${script_dir}/../bootstrap/install-forgejo-runner.sh"
  CI_TEMP_DIR=/usr/local install_forgejo_runner
  local runner_bin
  runner_bin=$(command -v forgejo-runner)

  ensure_lender_user

  local name labels
  name=${RUNNER_NAME:-lender-$(hostname -s)-$$}
  labels=$(host_labels "$RUNNER_LABELS")

  cat >"${RUNNER_DIR}/config.yml" <<EOF
log:
  level: info
  job_level: info
runner:
  file: .runner
  capacity: 1
  envs: {}
  timeout: 2h
  fetch_timeout: 30s
  fetch_interval: 2s
  labels: []
host:
  workdir_parent: ${RUNNER_DIR}/work
EOF
  chown "$LENDER_USER:$LENDER_USER" "${RUNNER_DIR}/config.yml"

  # The registration token is a command-line argument of this one short
  # process, which ends before the job can run; the job sees only the
  # per-runner token in .runner, valid for that runner alone.
  (cd "$RUNNER_DIR" && as_lender "$runner_bin" register --no-interactive --ephemeral \
    --config "${RUNNER_DIR}/config.yml" \
    --instance "$FORGEJO_INSTANCE_URL" \
    --token "$FORGEJO_RUNNER_REGISTRATION_TOKEN" \
    --name "$name" \
    --labels "$labels") || fail 'registration refused'

  printf 'Lending %s to %s as runner %s with labels %s for up to %s minutes\n' \
    "$(hostname -s)" "$FORGEJO_INSTANCE_URL" "$name" "$RUNNER_LABELS" "$LEND_TTL_MINUTES"

  # One job, then exit. The TTL bounds the wait for that job, not the job:
  # a job that has started (its working directory exists) runs to the end,
  # bounded by the runner's own timeout and the lender's job timeout.
  local status=0
  (cd "$RUNNER_DIR" && exec_as_lender "$runner_bin" one-job --wait --config "${RUNNER_DIR}/config.yml") &
  local runner_pid=$!
  local waited=0
  while kill -0 "$runner_pid" 2>/dev/null && ((waited < LEND_TTL_MINUTES * 60)); do
    if [[ -d "${RUNNER_DIR}/work" ]] && [[ -n "$(ls -A "${RUNNER_DIR}/work" 2>/dev/null)" ]]; then
      break
    fi
    sleep 5
    waited=$((waited + 5))
  done
  if kill -0 "$runner_pid" 2>/dev/null && ((waited >= LEND_TTL_MINUTES * 60)); then
    # INT stops polling; it does not deregister an unused ephemeral runner.
    # The second signal bounds a worker that will not stop.
    kill -INT "$runner_pid" 2>/dev/null || true
    sleep 30
    kill -KILL "$runner_pid" 2>/dev/null || true
    wait "$runner_pid" 2>/dev/null || true
    idle_expired "$name" "$LEND_TTL_MINUTES"
  fi
  wait "$runner_pid" || status=$?
  ((status == 0)) || fail "runner exited with status ${status}"
  printf 'Job finished; runner %s is done.\n' "$name"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
