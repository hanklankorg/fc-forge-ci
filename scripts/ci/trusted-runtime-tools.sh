#!/bin/bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Run only from the admitted immutable engine. The first argument names one of
# the finite tool profiles written here; the admitted workflow YAML states it
# as a literal, and nothing else chooses a directory: never caller PATH, a
# workspace or a job-install directory. This is tool placement, not CLI
# selection or a global environment sanitizer.
#
#   runtime         the approved runtime Containerfile's fixed tool directories
#   host            a hosted runner's root-owned system tools only
#   host-toolchain  host plus the hosted runner's documented product toolchain
#                   directories, the runner user's Cargo directory last, for
#                   the one key-free version-file preparation job; unqualified
#                   by design, see docs/engine-delivery.md
set -euo pipefail
refuse() {
  printf 'trusted-runtime-tools: %s\n' "$1" >&2
  exit 78
}
[[ $# -ge 2 ]] || refuse 'expected PROFILE COMMAND [ARGUMENT...]'
case "$1" in
runtime)
  export PATH=/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/bin:/bin
  export RUSTUP_HOME=/usr/local/rustup
  # The image's cargo executables/toolchain are shared read-only inputs. Registry
  # state belongs to this job's HOME.
  export CARGO_HOME=${HOME:?}/.cargo
  ;;
host) export PATH=/usr/bin:/bin ;;
host-toolchain)
  # HOME names a PATH directory here, so it must be one absolute directory.
  [[ ${HOME:?} == /* && $HOME != *:* ]] || refuse 'HOME must be an absolute path without a colon'
  export PATH=/usr/bin:/bin:/usr/local/bin:$HOME/.cargo/bin
  ;;
*) refuse "unknown tool profile: $1" ;;
esac
shift
[[ "$1" != forge-ci ]] || refuse 'explicit command required; forge-ci must be verified and absolute'
exec "$@"
