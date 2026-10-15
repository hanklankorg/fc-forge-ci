// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pathsafe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// OpenRoot opens an existing directory without accepting a symlink in the
// directory itself or any of its ancestors, except an operating-system layout
// link only the superuser can have placed (see systemLink). Each component is opened from its
// already-open parent and identity-checked, binding validation to the returned
// descriptor rather than trusting a pathname after an Lstat check.
func OpenRoot(name string) (*os.Root, error) {
	return openRoot(name, 0, false)
}

// MkdirRoot creates missing path components and returns a descriptor-backed
// root. Existing symlink components are rejected rather than followed.
func MkdirRoot(name string, perm fs.FileMode) (*os.Root, error) {
	return openRoot(name, perm, true)
}

// maxSystemLinks bounds how many operating-system layout links one path may
// pass through, so a loop of them is refused rather than followed forever.
const maxSystemLinks = 8

func openRoot(name string, perm fs.FileMode, create bool) (*os.Root, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("root path must be non-empty: %w", errs.ErrUsage)
	}

	abs, err := filepath.Abs(name)
	if err != nil {
		return nil, fmt.Errorf("resolve root %q: %w", name, err)
	}

	for range maxSystemLinks {
		root, next, walkErr := walkRoot(abs, name, perm, create)
		if walkErr != nil || next == "" {
			return root, walkErr
		}

		abs = next
	}

	return nil, fmt.Errorf("root %q passes through more than %d system links: %w", name, maxSystemLinks, errs.ErrValidation)
}

// walkRoot opens abs one component at a time from the volume root. It
// refuses a symlink unless it is an operating-system layout link, such as
// /var/run pointing at /run on Debian and Ubuntu, where runners keep their
// command files; then it returns the path through the link's target for the
// caller to walk again.
//
//nolint:cyclop,gocognit // every path component is identity-checked around optional creation.
func walkRoot(abs, name string, perm fs.FileMode, create bool) (*os.Root, string, error) {
	volumeRoot := filepath.VolumeName(abs) + string(filepath.Separator)

	rel, err := filepath.Rel(volumeRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, "", fmt.Errorf("resolve root %q from volume root: %w", name, errs.ErrValidation)
	}

	root, err := os.OpenRoot(volumeRoot)
	if err != nil {
		return nil, "", fmt.Errorf("open volume root for %q: %w", name, err)
	}

	if rel == "." {
		return root, "", nil
	}

	components := strings.Split(rel, string(filepath.Separator))
	walked := volumeRoot

	for index, component := range components {
		info, statErr := root.Lstat(component)
		if create && errors.Is(statErr, fs.ErrNotExist) {
			if mkdirErr := root.Mkdir(component, perm); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				_ = root.Close()

				return nil, "", fmt.Errorf("create root component %q in %q: %w", component, name, mkdirErr)
			}

			info, statErr = root.Lstat(component)
		}

		if statErr != nil {
			_ = root.Close()

			return nil, "", fmt.Errorf("inspect root component %q in %q: %w", component, name, statErr)
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			next, followed := systemLink(root, walked, component, info, components[index+1:])
			_ = root.Close()

			if !followed {
				return nil, "", fmt.Errorf("root component %q in %q is not a real directory: %w", component, name, errs.ErrValidation)
			}

			return nil, next, nil
		}

		if !info.IsDir() {
			_ = root.Close()

			return nil, "", fmt.Errorf("root component %q in %q is not a real directory: %w", component, name, errs.ErrValidation)
		}

		child, openErr := root.OpenRoot(component)
		if openErr != nil {
			_ = root.Close()

			return nil, "", fmt.Errorf("open root component %q in %q: %w", component, name, openErr)
		}

		opened, openedErr := child.Stat(".")

		current, currentErr := root.Lstat(component)
		if openedErr != nil || currentErr != nil || current.Mode()&fs.ModeSymlink != 0 ||
			!current.IsDir() || !os.SameFile(info, opened) || !os.SameFile(current, opened) {
			_ = child.Close()
			_ = root.Close()

			return nil, "", fmt.Errorf("root component %q in %q changed while opening: %w", component, name, errs.ErrValidation)
		}

		_ = root.Close()
		root = child
		walked = filepath.Join(walked, component)
	}

	return root, "", nil
}

// systemLink reports the path through a symlink that only the superuser can
// have placed: the link and the directory holding it are owned by root, and
// that directory is writable by nobody else. Such a link is part of the
// operating system's layout, not something a repository or an unprivileged
// user could plant; any other link is refused. The returned path joins the
// link's target, resolved against the directory holding it, with the rest.
func systemLink(parent *os.Root, parentPath, component string, link fs.FileInfo, rest []string) (string, bool) {
	directory, err := parent.Stat(".")
	if err != nil || !ownedByRoot(link) || !ownedByRoot(directory) || directory.Mode().Perm()&0o022 != 0 {
		return "", false
	}

	target, err := parent.Readlink(component)
	if err != nil || target == "" {
		return "", false
	}

	if !filepath.IsAbs(target) {
		target = filepath.Join(parentPath, target)
	}

	return filepath.Join(append([]string{filepath.Clean(target)}, rest...)...), true
}

// ownedByRoot reports whether info belongs to user 0.
func ownedByRoot(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)

	return ok && stat.Uid == 0
}
