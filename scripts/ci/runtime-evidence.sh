#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Bind the runtime handoff's artifact name and retention to the shared owner.
set -euo pipefail
exec forge-ci artifact upload --name "${1:?artifact name required}" \
  --dir "${2:?evidence directory required}" --retention-days 30 \
  --if-no-files error --verify-readback
