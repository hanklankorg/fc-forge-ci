// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package safeexec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ExecutableSHA256 identifies the executable a command would invoke without
// executing it. Symlinked tool installations are supported through LookPath.
func ExecutableSHA256(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("find builder executable %q: %w: %w", name, err, errs.ErrPrerequisite)
	}

	file, err := os.Open(path) //nolint:gosec // G304: the selected executable path returned by exec.LookPath.
	if err != nil {
		return "", fmt.Errorf("read builder executable: %w", err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat builder executable: %w", err)
	}

	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("builder executable is not a regular file: %w", errs.ErrValidation)
	}

	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("hash builder executable: %w", err)
	}

	return hex.EncodeToString(digest.Sum(nil)), nil
}
