// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package reporoot

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// WalkGoSources visits repository Go source, including standalone example
// modules. It skips Git metadata, root build/npm output and independent nested
// Go checkouts identified by both a regular go.mod and Git metadata. Directory
// names such as node_modules below a source package are not exemptions.
// The returned count is the number of source files passed to visit.
func WalkGoSources(root string, visit func(path, rel string) error) (int, error) {
	visited := 0
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, relErr := filepath.Rel(root, file)
		if relErr != nil {
			return relErr
		}

		rel = filepath.ToSlash(rel)

		if !entry.IsDir() {
			if filepath.Ext(entry.Name()) != ".go" {
				return nil
			}

			visited++

			return visit(file, rel)
		}

		if path.Base(rel) == ".git" || rel == "dist" || rel == "node_modules" {
			return filepath.SkipDir
		}

		if rel == "." {
			return nil
		}

		checkout, err := independentGoCheckout(file)
		if err != nil {
			return err
		}

		if checkout {
			return filepath.SkipDir
		}

		return nil
	})

	return visited, err
}

func independentGoCheckout(dir string) (bool, error) {
	module, err := os.Stat(filepath.Join(dir, "go.mod"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	if !module.Mode().IsRegular() {
		return false, nil
	}

	metadata, err := os.Stat(filepath.Join(dir, ".git"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return metadata.IsDir() || metadata.Mode().IsRegular(), nil
}
