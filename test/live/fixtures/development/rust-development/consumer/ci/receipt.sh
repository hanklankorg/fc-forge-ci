#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
# Live fixture: runs the receipt example in development-artifacts/. Called by
# produce.sh.
set -euo pipefail
cd development-artifacts
exec ../target/release/examples/receipt
