// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package osfs is a tiny filesystem adapter that backs the app/release
// CreateRelease release-notes check and the dist hand-off workspace.
// Production reads the real filesystem; CreateRelease tests pass an
// in-memory fake instead.
package osfs

import "os"

// FS satisfies app/release.fsOps and distWorkspace via the real OS filesystem.
type FS struct{}

// New returns the default OS-backed FS.
func New() *FS { return &FS{} }

// FileNonEmpty reports whether path exists and has size > 0.
func (f *FS) FileNonEmpty(path string) bool {
	if path == "" {
		return false
	}

	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	return info.Mode().IsRegular() && info.Size() > 0
}
