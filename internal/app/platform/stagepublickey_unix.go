// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build unix

package platform

import "syscall"

// syscallNoFollow refuses a symlink at the opened path, so a replaced source
// file cannot redirect the read.
const syscallNoFollow = syscall.O_NOFOLLOW
