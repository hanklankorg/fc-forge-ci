#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release-candidate checks, run by Forge CI's check job in a non-root,
# key-free image: the commands of the merge-request gate.
set -euo pipefail
sh -n install.sh
sh -n lib/greeting.sh
sh install.sh
