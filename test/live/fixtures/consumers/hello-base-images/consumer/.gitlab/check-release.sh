#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# Release-candidate checks, the release policy's `checks` input: the planner
# parses, and plans the root and its flavors without reaching a registry.
set -euo pipefail

bash -n base-plan.sh
bash base-plan.sh base registry.invalid/team/bases >/dev/null
bash base-plan.sh tools,debug registry.invalid/team/bases \
  "registry.invalid/team/bases@sha256:$(printf '%064d' 0)" "$(printf '%064d' 0)" >/dev/null
