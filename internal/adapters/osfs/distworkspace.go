// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package osfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ContainDistPath requires the assemble target to resolve inside the
// working directory.
//
// This path is downloaded into, digested, and — with --prune-dirs — walked with
// os.RemoveAll over every subdirectory. Its two neighbours in assemble-dist,
// release-images-path and each transfer item path, already go through the
// safe-relative-path rule; the one flag that deletes went unchecked beyond
// being non-empty and single-line, so `--path ../somewhere --prune-dirs`
// removed directories outside the workspace.
//
// Containment rather than a relative-path rule: an absolute path is fine as
// long as it lands inside. The command already treats the working directory as
// the workspace, since transfer item paths and the default "dist/" resolve
// against it, so anchoring here matches what everything else already assumes.
//
//nolint:cyclop // resolve cwd → resolve path → containment check → walk each component for a symlink escape. Phases of one containment decision.
func (f *FS) ContainDistPath(path string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("upload-dist: resolve working directory: %w", err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("upload-dist: resolve path %s: %w", path, err)
	}

	rel, err := filepath.Rel(cwd, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must stay inside the working directory: %s: %w", path, errs.ErrUsage)
	}

	current := cwd
	for component := range strings.SplitSeq(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)

		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) {
			break
		}

		if statErr != nil {
			return fmt.Errorf("upload-dist: inspect path component %s: %w", current, statErr)
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("upload-dist path contains symlink component: %s: %w", current, errs.ErrUsage)
		}
	}

	return nil
}

// AcquireDist returns the dist directory a hand-off digests: a real directory
// that resolves to a strict descendant of the working directory, with its
// top-level directories removed first when pruneDirs is set.
func (f *FS) AcquireDist(path string, pruneDirs bool) (string, error) {
	dir := strings.TrimSuffix(path, "/")

	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		if err != nil {
			return "", fmt.Errorf("upload-dist: %s is not a regular directory: %w", dir, err)
		}

		return "", fmt.Errorf("upload-dist: %s is not a regular directory: %w", dir, errs.ErrValidation)
	}

	if err := requireStrictDescendantOfCwd(dir); err != nil {
		return "", err
	}

	if !pruneDirs {
		return dir, nil
	}

	if err := pruneTopLevelDirs(dir); err != nil {
		return "", err
	}

	return dir, nil
}

func pruneTopLevelDirs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read upload-dist path %s: %w", dir, err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open upload-dist path %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if err := root.RemoveAll(entry.Name()); err != nil {
			return fmt.Errorf("prune upload-dist directory %s: %w", entry.Name(), err)
		}
	}

	return nil
}

func requireStrictDescendantOfCwd(path string) error {
	cwd, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("upload-dist: resolve working directory: %w", err)
	}

	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return fmt.Errorf("upload-dist: resolve working directory: %w", err)
	}

	resolved, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("upload-dist: resolve path %s: %w", path, err)
	}

	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return fmt.Errorf("upload-dist: resolve path %s: %w", path, err)
	}

	rel, err := filepath.Rel(cwd, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("upload-dist path must resolve to a strict descendant of the working directory: %s: %w", path, errs.ErrValidation)
	}

	return nil
}
