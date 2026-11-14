#!/bin/sh
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
. "$root/lib/greeting.sh"
greeting
