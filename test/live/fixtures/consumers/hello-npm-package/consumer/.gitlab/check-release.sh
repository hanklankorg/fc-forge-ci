#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Merge-request and release-candidate checks, run by Forge CI's check job in
# the non-root, key-free Node image. The tests pack the package and install
# the tarball into a scratch consumer offline; they gate here.
set -euo pipefail
npm ci --no-audit --no-fund
npm test
npm run build
