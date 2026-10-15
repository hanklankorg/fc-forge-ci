// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git_test

import (
	"os"
	"testing"

	adaptergit "github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// A workspace whose own name is an operating-system layout link -- the kind
// pathsafe.OpenRoot admits, such as /var/run pointing at /run -- is guarded
// through the link, so the re-check must look through it too. It used to
// compare the directory behind the link with the link itself and refuse every
// checkout there as replaced.
//
// Only the superuser can create such a link, so the test uses the host's own,
// as pathsafe's does. Nothing is written: the directory exists, so Open works
// in place, and finishing an in-place destination only re-checks it.
func TestCheckoutDestinations_FinishAcceptsAnExistingWorkspaceNamedByASystemLink(t *testing.T) {
	t.Parallel()

	const workspace = "/var/run"

	link, err := os.Lstat(workspace)
	if err != nil || link.Mode()&os.ModeSymlink == 0 {
		t.Skip("this host has no /var/run symlink")
	}

	root, err := pathsafe.OpenRoot(workspace)
	if err != nil {
		t.Skipf("this host's /var/run is not an admitted system link: %v", err)
	}

	_, gitErr := root.Lstat(".git")
	_ = root.Close()

	if gitErr == nil {
		t.Skip("this host's /var/run holds a .git")
	}

	dir, finish, err := adaptergit.CheckoutDestinations{}.Open(workspace)
	if err != nil {
		t.Fatalf("Open(%s) error = %v", workspace, err)
	}

	if dir != workspace {
		t.Fatalf("Open(%s) works in %s, want the existing workspace in place", workspace, dir)
	}

	if err := finish(true); err != nil {
		t.Errorf("finish(true) error = %v, want the unchanged workspace accepted", err)
	}
}

// A checkout into a new directory is staged in an owner-only temporary
// directory, and that mode used to survive the rename. Nanolinter's image,
// which reads the checkout as its own user, then failed every check with
// "Permission denied: '/workspace/.git'". The published tree is readable by
// others, like a git clone.
func TestCheckoutDestinations_PublishedCheckoutIsReadableByOthers(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir() + "/src"

	dir, finish, err := adaptergit.CheckoutDestinations{}.Open(workspace)
	if err != nil {
		t.Fatalf("Open(%s) error = %v", workspace, err)
	}

	if dir == workspace {
		t.Fatalf("Open(%s) works in place, want a staged checkout", workspace)
	}

	if finishErr := finish(true); finishErr != nil {
		t.Fatalf("finish(true) error = %v", finishErr)
	}

	info, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}

	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("published checkout mode = %o, want 755", got)
	}
}
