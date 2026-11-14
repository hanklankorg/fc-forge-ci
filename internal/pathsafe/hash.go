// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pathsafe

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// HashRegularFile returns the SHA-256 digest and size of an existing regular
// file. Its directory is opened with OpenRoot and the file itself must not be
// a symlink or change between the check and the open.
func HashRegularFile(path string) ([]byte, int64, error) {
	root, err := OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = root.Close() }()

	name := filepath.Base(path)

	before, err := root.Lstat(name)
	if err != nil {
		return nil, 0, err
	}

	if !before.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s is not a regular file: %w", path, errs.ErrValidation)
	}

	file, err := root.Open(name)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()

	opened, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}

	if !os.SameFile(before, opened) {
		return nil, 0, fmt.Errorf("%s changed while opening: %w", path, errs.ErrValidation)
	}

	digest := sha256.New()

	size, err := io.Copy(digest, file)
	if err != nil {
		return nil, 0, err
	}

	return digest.Sum(nil), size, nil
}
