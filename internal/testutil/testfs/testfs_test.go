// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package testfs_test

import (
	"io/fs"
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func TestMemory_ListsParentDirectories(t *testing.T) {
	fsys := testfs.NewMemory(t)
	fsys.WriteFile(".github/workflows/ok.yml", []byte("name: ok\n"))

	entries, err := fs.ReadDir(fsys.FS(), ".github/workflows")
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 || entries[0].Name() != "ok.yml" {
		t.Fatalf("entries = %+v", entries)
	}
}

// The tests above prove the helper stores what it was given and hands it back.
// They do not prove it keeps its own copy, and a fixture helper that shares a
// slice with the test is a way for one assertion to change what a later one
// reads — the sort of failure that only shows up when the test order changes.
//
// Nor did anything prove MkdirAll does its job: making it a no-op left every
// test that builds a directory tree with it passing, because most of them go
// on to write files, and writing a file creates its parents anyway. The cases
// where the directory itself is the fixture — an empty directory a validator
// is supposed to reject, a tree walked for its shape — are exactly the ones a
// silent no-op would break.

func TestMemory_WriteFileCopiesTheCallerSlice(t *testing.T) {
	t.Parallel()

	fsys := testfs.NewMemory(t)

	body := []byte("original")
	fsys.WriteFile("a/b.txt", body)

	// A caller that reuses its buffer must not rewrite what was stored.
	copy(body, "MUTATED!")

	if got := string(fsys.ReadFile("a/b.txt")); got != "original" {
		t.Errorf("stored content = %q, want %q: the helper kept the caller's slice", got, "original")
	}
}

func TestMemory_ReadFileReturnsACopy(t *testing.T) {
	t.Parallel()

	fsys := testfs.NewMemory(t)
	fsys.WriteFile("a.txt", []byte("original"))

	got := fsys.ReadFile("a.txt")
	copy(got, "MUTATED!")

	if second := string(fsys.ReadFile("a.txt")); second != "original" {
		t.Errorf("second read = %q, want %q: the helper handed out its own storage", second, "original")
	}
}

func TestMemory_MkdirAllRecordsTheDirectoryAndItsParents(t *testing.T) {
	t.Parallel()

	fsys := testfs.NewMemory(t)
	fsys.MkdirAll("a/b/c")

	for _, dir := range []string{"a", "a/b", "a/b/c"} {
		info, err := fs.Stat(fsys.FS(), dir)
		if err != nil {
			t.Fatalf("stat %q: %v", dir, err)
		}

		if !info.IsDir() {
			t.Errorf("%q is not a directory", dir)
		}
	}
}

// TestReal_PathDoesNotCreateAnything separates joining from creating. Path is
// used to name a file that is supposed to be ABSENT — "refuses a missing
// config" fixtures pass it a path and expect nothing there. If it created
// what it named, those tests would be asserting the opposite of their name.
func TestReal_PathDoesNotCreateAnything(t *testing.T) {
	t.Parallel()

	fsys := testfs.NewReal(t)
	p := fsys.Path("nested", "missing.txt")

	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("Path created %q (stat err = %v)", p, err)
	}

	if _, err := os.Stat(fsys.Path("nested")); !os.IsNotExist(err) {
		t.Errorf("Path created the parent directory")
	}
}
