// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build !linux

package safeexec

// HardenProcess is a no-op on non-Linux platforms, including the macOS build
// and publishing jobs. The Linux implementation sets RLIMIT_CORE and
// PR_SET_DUMPABLE; those best-effort protections are not implemented here.
func HardenProcess() {}
