// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// CheckoutDestinations owns the directory a checkout's Git work runs in, from
// the guard before the first Git call to publication or rollback after the
// last. It satisfies app/platform.CheckoutDestinations structurally.
type CheckoutDestinations struct{}

// Inspect refuses a workspace that is linked, not a directory, or already holds
// a .git, without creating anything. A missing workspace is accepted.
func (CheckoutDestinations) Inspect(workspace string) error {
	return guardCheckoutWorkspace(workspace, false)
}

// Open prepares the directory the Git work runs in and returns it with the one
// call that ends its lifetime: finish(true) publishes a staged checkout,
// finish(false) removes it, and either refuses a directory that was replaced
// after it was guarded. A destination that already existed is used in place and
// left as the run left it.
func (CheckoutDestinations) Open(workspace string) (string, func(ok bool) error, error) {
	destination, err := openCheckoutDestination(workspace)
	if err != nil {
		return "", nil, err
	}

	return destination.dir, destination.finish, nil
}

// checkoutDestination is where the Git work actually happens and how it reaches
// the caller's workspace.
//
// A checkout is a long sequence of external Git commands, and any of them can
// fail. Running them straight into the destination means a failure leaves a
// half-populated repository there, which the next run has to reason about. When
// this run is the one creating the destination, that is avoidable: the work goes
// into a sibling staging directory and is renamed into place only once it
// succeeded, so a failure leaves nothing behind and a retry starts clean.
//
// When the destination already exists it belongs to the caller, who may have put
// files there deliberately. Renaming over it would destroy them, so that case
// keeps working in place, and a failure keeps the partial state it always did.
//
// One thing this cannot prevent, only detect. Git takes a pathname, not a
// directory descriptor, so the directory guarded before the run is not provably
// the directory Git writes to: anything with write access to the parent can
// swap the path in between. `identity` is the guarded directory's inode, taken
// at guard time and re-checked before publication. A swap is then a refusal
// with a diagnostic instead of a checkout published into somewhere nobody
// checked. Detecting it after the fact is weaker than preventing it, and it is
// what a pathname-based tool allows.
//
// The comparison is by inode, so it sees a swap — a rename or a symlink
// redirect, which is what a hostile replacement is — and it does NOT reliably
// see a delete-and-recreate at the same path, because the filesystem may hand
// the new directory the inode the old one just freed. Closing that would need
// Git to accept a directory descriptor, which it does not.
type checkoutDestination struct {
	dir      string
	final    string
	staged   bool
	identity os.FileInfo
	rollback func() error
}

// openCheckoutDestination prepares the directory the Git work will run in.
func openCheckoutDestination(workspace string) (checkoutDestination, error) {
	root, err := pathsafe.OpenRoot(workspace)
	if err == nil {
		_ = root.Close()

		if guardErr := guardCheckoutWorkspace(workspace, true); guardErr != nil {
			return checkoutDestination{}, guardErr
		}

		identity, statErr := os.Stat(workspace)
		if statErr != nil {
			return checkoutDestination{}, fmt.Errorf("record checkout workspace identity for %s: %w", workspace, statErr)
		}

		return checkoutDestination{dir: workspace, final: workspace, identity: identity}, nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return checkoutDestination{}, fmt.Errorf("open checkout workspace %s: %w", workspace, err)
	}

	// The destination's parent may not exist either; creating it is the same
	// component-safe step the in-place path used to do for the whole chain.
	parent, err := pathsafe.MkdirRoot(filepath.Dir(workspace), 0o755)
	if err != nil {
		return checkoutDestination{}, fmt.Errorf("create checkout workspace parent for %s: %w", workspace, err)
	}

	_ = parent.Close()

	staging, err := os.MkdirTemp(parent.Name(), "."+filepath.Base(workspace)+"-staging-")
	if err != nil {
		return checkoutDestination{}, fmt.Errorf("create checkout staging directory beside %s: %w", workspace, err)
	}

	if guardErr := guardCheckoutWorkspace(staging, true); guardErr != nil {
		return checkoutDestination{}, errors.Join(guardErr, os.RemoveAll(staging))
	}

	identity, err := os.Stat(staging)
	if err != nil {
		return checkoutDestination{}, errors.Join(
			fmt.Errorf("record checkout staging identity for %s: %w", staging, err), os.RemoveAll(staging))
	}

	return checkoutDestination{
		dir: staging, final: workspace, staged: true, identity: identity,
		rollback: func() error { return os.RemoveAll(staging) },
	}, nil
}

// stillTheGuardedDirectory reports whether the path this run has been writing
// to is the same directory that was guarded before it started.
func (d checkoutDestination) stillTheGuardedDirectory() error {
	if d.identity == nil {
		return nil
	}

	current, err := os.Lstat(d.dir)
	if err != nil {
		return fmt.Errorf("re-check checkout destination %s: %w", d.dir, err)
	}

	if !os.SameFile(d.identity, current) && !d.reachedThroughAdmittedLink(current) {
		return fmt.Errorf("checkout destination %s was replaced after it was checked; refusing to publish work from an unverified directory: %w",
			d.dir, errs.ErrValidation)
	}

	return nil
}

// reachedThroughAdmittedLink reports whether the path is a link the guard
// itself follows and still leads to the guarded directory. The identity is the
// directory behind such a link, while the re-check observes the link, so a
// workspace named by an operating-system layout link would otherwise always
// look replaced. pathsafe.OpenRoot is the rule the guard admitted the path
// under; any link it refuses is still a replacement.
func (d checkoutDestination) reachedThroughAdmittedLink(current os.FileInfo) bool {
	if current.Mode()&os.ModeSymlink == 0 {
		return false
	}

	root, err := pathsafe.OpenRoot(d.dir)
	if err != nil {
		return false
	}

	defer func() { _ = root.Close() }()

	opened, err := root.Stat(".")

	return err == nil && os.SameFile(d.identity, opened)
}

// finish publishes a staged checkout, or removes it when the run failed. The
// rename is the publication: until it happens the destination does not exist,
// and after it the checkout is complete.
func (d checkoutDestination) finish(ok bool) error {
	// Checked on both paths, and before the rollback: a swapped path must not
	// be published, and must not be deleted either.
	if identityErr := d.stillTheGuardedDirectory(); identityErr != nil {
		return identityErr
	}

	if !d.staged {
		return nil
	}

	if !ok {
		if err := d.rollback(); err != nil {
			return fmt.Errorf("remove failed checkout staging directory: %w", err)
		}

		return nil
	}

	// MkdirTemp made the staging directory owner-only, which suits a checkout
	// in progress but not the published tree: kept, it locked out every other
	// reader, such as a lint image that runs as another user. Published, it
	// gets 0755, the mode its parent is created with (before umask), so other
	// users can read it as they can a git clone or actions/checkout.
	if err := os.Chmod(d.dir, 0o755); err != nil { //nolint:gosec // a published checkout is world-readable by convention.
		return errors.Join(fmt.Errorf("set checkout mode on %s: %w", d.dir, err), d.rollback())
	}

	if err := os.Rename(d.dir, d.final); err != nil {
		return errors.Join(
			fmt.Errorf("publish checkout to %s: %w", d.final, err),
			d.rollback(),
		)
	}

	return nil
}

// Missing components are harmless during preflight, but links, non-directories
// and inspection errors are not absence. Creation rechecks components and .git.
func guardCheckoutWorkspace(workspace string, create bool) error {
	open := pathsafe.OpenRoot
	if create {
		open = func(name string) (*os.Root, error) { return pathsafe.MkdirRoot(name, 0o755) }
	}

	root, err := open(workspace)
	if !create && errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("open checkout workspace %s: %w", workspace, err)
	}

	defer func() { _ = root.Close() }()

	if _, err := root.Lstat(".git"); err == nil {
		return fmt.Errorf("workspace %s already contains a .git: %w", workspace, errs.ErrValidation)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect checkout .git: %w", err)
	}

	return nil
}
