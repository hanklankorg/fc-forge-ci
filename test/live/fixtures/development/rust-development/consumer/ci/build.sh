#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
# Live fixture: tests and builds the static Rust binaries offline and checks
# their versions. Called by produce.sh.
set -euo pipefail

test "$(uname -s)" = Linux
test "$(uname -m)" = x86_64
export RUSTFLAGS='-C target-feature=+crt-static'
cargo test --locked --offline
cargo build --release --bins --locked --offline
cargo build --release --example receipt --locked --offline
for name in worker key-tool api-service load-generator; do
  test "$("target/release/$name" --version)" = "$name ${FIXTURE_VERSION:-0.1.0}"
done
