# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# A library that install.sh sources; it is not run on its own.
# shellcheck shell=sh

greeting() {
  printf '%s\n' 'Hello from a Git-distributed script library'
}
