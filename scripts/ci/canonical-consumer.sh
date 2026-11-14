#!/bin/bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Receiver glue only. Source must be a fresh administrator-pinned engine checkout.
# Verification/transport admission remain owned by canonical-cli-delivery.sh.
set -euo pipefail
export PATH=/usr/bin:/bin
[[ $# == 1 && ("$1" == prepare || "$1" == admit || "$1" == receive) ]]
[[ "${CANONICAL_PRODUCER_RESULT:-}" == success ]]
# The admitted workflow states the tool profile as a literal; the helper owns
# the finite set and refuses an unknown name at the first verified probe.
[[ "${TOOL_PROFILE:-}" =~ ^[a-z][a-z-]*$ ]]
export ENGINE_CHECKOUT=${RUNNER_TEMP:?}/forge-ci-approved-engine
if [[ "$1" == prepare ]]; then
  [[ ! -e "$ENGINE_CHECKOUT" && ! -L "$ENGINE_CHECKOUT" ]]
  # Retain the authenticated checkout outside the consumer workspace, while
  # leaving the composite action's source in place until that action completes.
  cp -a -- "${GITHUB_WORKSPACE:?}/_forge_ci_bootstrap" "$ENGINE_CHECKOUT"
fi
/bin/bash -p "$ENGINE_CHECKOUT/scripts/ci/canonical-cli-delivery.sh" "$1"
[[ "$1" == receive ]] || exit 0

# The original binary has passed metadata, inventory, raw hash and bootstrap.
# Each subsequent invocation repeats the existing strict absolute-file verifier.
# Keep the engine/launcher outside consumer checkout, which may replace workspace.
# Delivery admitted the file of this host's architecture; the launcher names it.
case "$(uname -m)" in
x86_64) arch=amd64 ;;
aarch64) arch=arm64 ;;
*) exit 78 ;;
esac
directory=$RUNNER_TEMP/forge-ci-canonical-bin
[[ ! -e "$directory" && ! -L "$directory" ]]
mkdir -m0700 -- "$directory"
printf '#!/bin/bash -p\nexec /bin/bash -p %q %q %q %q %q %q -- "$@"\n' \
  "$ENGINE_CHECKOUT/scripts/ci/trusted-bootstrap.sh" "$ENGINE_CHECKOUT" \
  "${ENGINE_ORIGIN:?}" "${ENGINE_COMMIT:?}" \
  "$RUNNER_TEMP/forge-ci-canonical-${GITHUB_RUN_ID:?}-${GITHUB_RUN_ATTEMPT:?}/forge-ci-linux-$arch" \
  "$TOOL_PROFILE" >"$directory/forge-ci"
chmod 0700 -- "$directory/forge-ci"
printf '%s\n' "$directory" >>"${GITHUB_PATH:?}"
printf 'verified=true\n' >>"${GITHUB_OUTPUT:?}"
