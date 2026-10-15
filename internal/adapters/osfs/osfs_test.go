// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package osfs_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/osfs"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func TestFS_FileNonEmpty(t *testing.T) {
	t.Parallel()

	fsys := testfs.NewReal(t)
	empty := fsys.WriteFile("empty.txt", nil)
	nonEmpty := fsys.WriteFile("artifact.jar", []byte("jar"))
	dir := fsys.MkdirAll("dir")
	adapter := osfs.New()

	if adapter.FileNonEmpty(empty) {
		t.Fatal("empty file should not be non-empty")
	}

	if !adapter.FileNonEmpty(nonEmpty) {
		t.Fatal("non-empty file should be non-empty")
	}

	if adapter.FileNonEmpty(dir) || adapter.FileNonEmpty(fsys.Path("missing")) || adapter.FileNonEmpty("") {
		t.Fatal("directories, missing paths, and empty paths should not be non-empty files")
	}
}
