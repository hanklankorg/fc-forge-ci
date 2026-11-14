#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Merge-request and release-candidate checks, run by Forge CI's check job in
# the non-root, key-free Node image. The tests gate here: the release build
# runs them too, but only warns when they fail.
set -euo pipefail
npm ci --no-audit --no-fund
npm test
npm run build
