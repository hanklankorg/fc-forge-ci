#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Merge-request and release-candidate checks, run by Forge CI's check job in
# the non-root, key-free Java image.
set -euo pipefail
mvn --batch-mode --no-transfer-progress --errors verify
