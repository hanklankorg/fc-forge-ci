// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"

	domainartifact "github.com/diggsweden/forge-ci/internal/domain/artifact"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// GatherExportsInput joins pre-manifest producer trees, never signer handoffs.
type GatherExportsInput struct {
	Sources               []string
	Destination           string
	Enabled, AllowMissing bool
}

type exportEntry struct {
	root *os.Root
	path string
	info fs.FileInfo
}

// GatherExports validates every tree and top-level collision before copying.
// Descriptor-rooted operations and exclusive creation prevent link escapes and
// overwrites. It intentionally does not compute a signer dist digest yet.
func GatherExports(ctx context.Context, in GatherExportsInput) error {
	return gatherExportTrees(ctx, in, nil)
}

func gatherExportTrees(ctx context.Context, in GatherExportsInput, destination *os.Root) error {
	if !in.Enabled {
		return nil
	}

	if len(in.Sources) == 0 || in.Destination == "" {
		return fmt.Errorf("export sources and destination required: %w", errs.ErrUsage)
	}

	var roots []*os.Root
	defer func() {
		for _, root := range roots {
			_ = root.Close()
		}
	}()

	var entries []exportEntry

	names := map[string]bool{}

	var total int64

	files := 0

	for _, source := range in.Sources {
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			if in.AllowMissing {
				continue
			}

			return fmt.Errorf("export source %s does not exist; the export commands must write it: %w", source, errs.ErrValidation)
		}

		if err != nil {
			return err
		}

		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("export source must be a directory of regular files: %w", errs.ErrValidation)
		}

		root, err := pathsafe.OpenRoot(source)
		if err != nil {
			return err
		}

		roots = append(roots, root)

		err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}

			if err := ctx.Err(); err != nil {
				return err
			}

			if path == "." {
				return nil
			}

			info, err := entry.Info()
			if err != nil {
				return err
			}

			if !info.IsDir() && !info.Mode().IsRegular() {
				return fmt.Errorf("export may hold only regular files and directories: %w", errs.ErrValidation)
			}

			if !strings.Contains(path, "/") {
				if names[path] {
					return fmt.Errorf("export path %s must not exist yet: %w", path, errs.ErrValidation)
				}

				names[path] = true
			}

			if info.Mode().IsRegular() {
				files++

				total += info.Size()
				if info.Size() > domainartifact.MaxFileBytes || total > domainartifact.MaxTotalBytes || files > domainartifact.MaxFileCount {
					return fmt.Errorf("export exceeds artifact limits: %w", errs.ErrValidation)
				}
			}

			entries = append(entries, exportEntry{root: root, path: path, info: info})

			return nil
		})
		if err != nil {
			return err
		}
	}

	if destination == nil {
		root, err := pathsafe.MkdirRoot(in.Destination, 0o755)
		if err != nil {
			return err
		}

		destination = root
		defer func() { _ = root.Close() }()
	}

	for name := range names {
		if _, err := destination.Lstat(name); !os.IsNotExist(err) {
			return fmt.Errorf("export path %s must not exist yet: %w", name, errs.ErrValidation)
		}
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}

		if entry.info.IsDir() {
			if err := destination.Mkdir(entry.path, 0o755); err != nil {
				return err
			}

			continue
		}

		if err := copyExportFile(destination, entry); err != nil {
			return err
		}
	}

	return nil
}

func copyExportFile(destination *os.Root, entry exportEntry) error {
	source, err := entry.root.Open(entry.path)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	info, err := source.Stat()
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() || !os.SameFile(info, entry.info) || info.Size() != entry.info.Size() {
		return fmt.Errorf("export changed during collection: %w", errs.ErrValidation)
	}

	out, err := destination.OpenFile(entry.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0o777)
	if err != nil {
		return err
	}

	n, copyErr := domainartifact.CopyAtMost(out, source, entry.info.Size())
	closeErr := out.Close()

	if copyErr != nil {
		return copyErr
	}

	if n != entry.info.Size() {
		return fmt.Errorf("export changed during copy: %w", errs.ErrValidation)
	}

	return closeErr
}
