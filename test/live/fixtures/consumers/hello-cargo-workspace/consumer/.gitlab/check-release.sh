#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Merge-request and release-candidate checks, run by Forge CI's check job in
# the non-root, key-free Rust image. The workspace has no crate to fetch.
set -euo pipefail
cargo test --workspace --locked --offline
cargo build --workspace --release --locked --offline
