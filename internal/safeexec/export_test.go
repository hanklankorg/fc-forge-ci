// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package safeexec

import "testing"

// UseProcSwapsFixture points the kernel swap reader at path for one test, so
// tests can exercise kernel semantics without the reported override.
func UseProcSwapsFixture(t *testing.T, path string) {
	t.Helper()

	previous := procSwapsPath
	procSwapsPath = path

	t.Cleanup(func() { procSwapsPath = previous })
}

// UseCgroupFixture points the cgroup reader at a /proc/self/cgroup file and a
// cgroup v2 mount root for one test.
func UseCgroupFixture(t *testing.T, selfCgroup, root string) {
	t.Helper()

	previousSelf, previousRoot := procSelfCgroupPath, cgroupRoot
	procSelfCgroupPath, cgroupRoot = selfCgroup, root

	t.Cleanup(func() { procSelfCgroupPath, cgroupRoot = previousSelf, previousRoot })
}

// UseInteractive fixes the terminal check for one test.
func UseInteractive(t *testing.T, value bool) {
	t.Helper()

	previous := interactive
	interactive = func() bool { return value }

	t.Cleanup(func() { interactive = previous })
}
