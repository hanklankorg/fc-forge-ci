#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: CC0-1.0
set -euo pipefail
test "$(id -u)" = 65532
test "${CARGO_BUILD_JOBS:-}" = 2
test "$(rustc --version | cut -d ' ' -f2)" = 1.99.1
rustup target list --installed | grep -Fx x86_64-unknown-linux-gnu
# Includes the original inert VM-controller contract prerequisites. Hypervisors
# are mocked by that test; do not install them in the job image.
for tool in bash git jq just ssh ssh-keygen cc pkg-config msgfmt msgmerge ps tar sha256sum timeout; do
	command -v "${tool}"
done
ps -p "$$" -o pid= >/dev/null
test -d /opt/kics/assets/queries/dockerfile
test -d /opt/kics/assets/libraries
kics version
printf '%s\n' 'GENERIC_CLI_RUST_JOB_CAPABILITIES_OK'
