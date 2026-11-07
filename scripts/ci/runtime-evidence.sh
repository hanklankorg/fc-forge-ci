#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

# Bind the runtime handoff's artifact name and retention to the shared owner.
set -euo pipefail
exec forge-ci artifact upload --name "${1:?artifact name required}" \
	--dir "${2:?evidence directory required}" --retention-days 30 \
	--if-no-files error --verify-readback
