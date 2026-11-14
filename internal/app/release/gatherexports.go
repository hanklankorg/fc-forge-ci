// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
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

type exportInventory struct {
	entries []exportEntry
	names   map[string]bool
	roots   []*os.Root
	total   int64
	files   int
}

func (inventory *exportInventory) close() {
	for _, root := range inventory.roots {
		_ = root.Close()
	}
}

func (inventory *exportInventory) addTree(ctx context.Context, root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
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

		return inventory.addEntry(root, path, info)
	})
}

func (inventory *exportInventory) addEntry(root *os.Root, path string, info fs.FileInfo) error {
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("export may hold only regular files and directories: %w", errs.ErrValidation)
	}

	if !strings.Contains(path, "/") {
		if inventory.names[path] {
			return fmt.Errorf("export path %s must not exist yet: %w", path, errs.ErrValidation)
		}

		inventory.names[path] = true
	}

	if info.Mode().IsRegular() {
		inventory.files++
		inventory.total += info.Size()

		if info.Size() > domainartifact.MaxFileBytes || inventory.total > domainartifact.MaxTotalBytes || inventory.files > domainartifact.MaxFileCount {
			return fmt.Errorf("export exceeds artifact limits: %w", errs.ErrValidation)
		}
	}

	inventory.entries = append(inventory.entries, exportEntry{root: root, path: path, info: info})

	return nil
}

// GatherExports validates every tree and top-level collision before copying.
// Descriptor-rooted operations and exclusive creation prevent link escapes and
// overwrites. It intentionally does not compute a signer dist digest yet.
func GatherExports(ctx context.Context, in GatherExportsInput) error {
	return gatherExportTrees(ctx, in, nil)
}

func gatherExportTrees(ctx context.Context, in GatherExportsInput, destination *os.Root) error {
	return gatherExportTreesAdmitted(ctx, in, destination, nil)
}

func gatherExportTreesAdmitted(ctx context.Context, in GatherExportsInput, destination *os.Root, admit func(fs.FS) error) error {
	if !in.Enabled {
		return nil
	}

	if len(in.Sources) == 0 || in.Destination == "" {
		return fmt.Errorf("export sources and destination required: %w", errs.ErrUsage)
	}

	inventory, err := collectExportInventory(ctx, in, admit)
	if err != nil {
		return err
	}
	defer inventory.close()

	if destination == nil {
		root, err := pathsafe.MkdirRoot(in.Destination, 0o755)
		if err != nil {
			return err
		}

		destination = root
		defer func() { _ = root.Close() }()
	}

	for name := range inventory.names {
		if _, err := destination.Lstat(name); !os.IsNotExist(err) {
			return fmt.Errorf("export path %s must not exist yet: %w", name, errs.ErrValidation)
		}
	}

	for _, entry := range inventory.entries {
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

func collectExportInventory(ctx context.Context, in GatherExportsInput, admit func(fs.FS) error) (*exportInventory, error) {
	inventory := &exportInventory{names: map[string]bool{}}

	complete := false
	defer func() {
		if !complete {
			inventory.close()
		}
	}()

	for _, source := range in.Sources {
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			if in.AllowMissing {
				continue
			}

			return nil, fmt.Errorf("export source %s does not exist; the export commands must write it: %w", source, errs.ErrValidation)
		}

		if err != nil {
			return nil, err
		}

		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("export source must be a directory of regular files: %w", errs.ErrValidation)
		}

		root, err := pathsafe.OpenRoot(source)
		if err != nil {
			return nil, err
		}

		inventory.roots = append(inventory.roots, root)

		if admit != nil {
			if err := admit(root.FS()); err != nil {
				return nil, err
			}
		}

		if err := inventory.addTree(ctx, root); err != nil {
			return nil, err
		}
	}

	complete = true

	return inventory, nil
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
