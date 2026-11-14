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
	"golang.org/x/sys/unix"
)

// OpenRoot opens an existing directory, refusing links except root-owned system
// layout links in root-owned, non-writable parents. Ancestors need only search.
func OpenRoot(name string) (*os.Root, error) { return openRoot(name, 0, false) }

// MkdirRoot creates missing real directories and returns the destination root.
func MkdirRoot(name string, perm fs.FileMode) (*os.Root, error) { return openRoot(name, perm, true) }

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
		root, next, err := walkRoot(abs, name, perm, create)
		if err != nil || next == "" {
			return root, err
		}

		abs = next
	}

	return nil, fmt.Errorf("root %q passes through more than %d system links: %w", name, maxSystemLinks, errs.ErrValidation)
}

func walkRoot(abs, name string, perm fs.FileMode, create bool) (*os.Root, string, error) {
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open traversal root for %q: %w", name, err)
	}

	current := os.NewFile(uintptr(fd), "/")
	defer func() { _ = current.Close() }()

	walked := "/"

	components := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	for index, component := range components {
		if component == "" {
			continue
		}

		child, err := openRootEntry(current, component, perm, create)
		if err != nil {
			return nil, "", fmt.Errorf("open root component %q in %q: %w", component, name, err)
		}

		info, err := child.Stat()
		if err != nil {
			_ = child.Close()

			return nil, "", err
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			next, followed := systemLink(current, walked, component, info, components[index+1:])
			_ = child.Close()

			if !followed {
				return nil, "", fmt.Errorf("root component %q in %q is not a real directory: %w", component, name, errs.ErrValidation)
			}

			return nil, next, nil
		}

		if !info.IsDir() {
			_ = child.Close()

			return nil, "", fmt.Errorf("root component %q in %q is not a real directory: %w", component, name, errs.ErrValidation)
		}

		_ = current.Close()
		current = child
		walked = filepath.Join(walked, component)
	}

	root, err := rootFromDirectory(current, walked)

	return root, "", err
}

func openRootEntry(parent *os.File, name string, perm fs.FileMode, create bool) (*os.File, error) {
	const flags = unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC

	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if create && errors.Is(err, unix.ENOENT) {
		if err := unix.Mkdirat(int(parent.Fd()), name, directoryMode(perm)); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}

		fd, err = unix.Openat(int(parent.Fd()), name, flags, 0)
	}

	if err != nil {
		return nil, err
	}

	return os.NewFile(uintptr(fd), name), nil
}

func directoryMode(perm fs.FileMode) uint32 {
	mode := uint32(perm.Perm())
	if perm&fs.ModeSetuid != 0 {
		mode |= unix.S_ISUID
	}

	if perm&fs.ModeSetgid != 0 {
		mode |= unix.S_ISGID
	}

	if perm&fs.ModeSticky != 0 {
		mode |= unix.S_ISVTX
	}

	return mode
}

func rootFromDirectory(directory *os.File, path string) (*os.Root, error) {
	expected, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	// os.Root cannot adopt a descriptor. Prevent another user's ancestor swap
	// before returning mutation capabilities; read only the final destination.
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}

	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(expected, opened) {
		_ = root.Close()

		return nil, fmt.Errorf("root %q changed while opening: %w", path, errs.ErrValidation)
	}

	return root, nil
}

func systemLink(parent *os.File, parentPath, component string, link fs.FileInfo, rest []string) (string, bool) {
	directory, err := parent.Stat()
	if err != nil || !ownedByRoot(link) || !ownedByRoot(directory) || directory.Mode().Perm()&0o022 != 0 {
		return "", false
	}
	// Linux limits a symlink target to less than PATH_MAX bytes. Refuse a full
	// buffer instead of ever adopting a truncated target.
	var buffer [unix.PathMax]byte

	n, err := unix.Readlinkat(int(parent.Fd()), component, buffer[:])
	if err != nil || n == 0 || n == len(buffer) {
		return "", false
	}

	target := string(buffer[:n])
	if !filepath.IsAbs(target) {
		target = filepath.Join(parentPath, target)
	}

	return filepath.Join(append([]string{filepath.Clean(target)}, rest...)...), true
}

func ownedByRoot(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)

	return ok && stat.Uid == 0
}
