// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pathsafe_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"golang.org/x/sys/unix"
)

func TestRootNeedsNoAncestorReadAccess(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission bits")
	}

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ancestor := filepath.Join(base, "searchable")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(ancestor, 0o700) })

	if err := os.Chmod(ancestor, 0o300); err != nil {
		t.Fatal(err)
	}

	fd, err := unix.Open(ancestor, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err == nil {
		_ = unix.Close(fd)
	}

	if !errors.Is(err, unix.EACCES) {
		t.Fatalf("fixture must deny ancestor reads: %v", err)
	}

	destination := filepath.Join(ancestor, "created", "root")

	root, err := pathsafe.MkdirRoot(destination, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	if err := root.WriteFile("marker", []byte("private\n"), 0o600); err != nil {
		_ = root.Close()

		t.Fatal(err)
	}

	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	root, err = pathsafe.OpenRoot(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	content, err := root.ReadFile("marker")
	if err != nil || string(content) != "private\n" {
		t.Fatalf("read through destination root: %q %v", content, err)
	}

	if _, err := pathsafe.OpenRoot(filepath.Join(ancestor, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path lost its cause: %v", err)
	}
}
