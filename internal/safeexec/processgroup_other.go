// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build !linux

package safeexec

import "os/exec"

// configureProcessGroup keeps exec's default cancellation off Linux.
func configureProcessGroup(*exec.Cmd) {}
