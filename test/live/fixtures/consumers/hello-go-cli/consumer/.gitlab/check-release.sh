#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Release-candidate checks, run by Forge CI's check job in the non-root,
# key-free Go image: the commands of the merge-request gate.
set -euo pipefail
go vet ./...
go test ./...
