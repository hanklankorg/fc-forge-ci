// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"fmt"
	"path/filepath"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// MavenProjectDirectory is the workspace-confined project used by both Maven
// metadata and subprocess execution. It never changes the process directory.
func MavenProjectDirectory(directory string) (string, error) {
	if !pathsafe.Relative(directory) {
		return "", fmt.Errorf("working-directory must be workspace-relative without parent-directory steps: %w", errs.ErrUsage)
	}

	root, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return "", fmt.Errorf("maven project directory: %w", err)
	}

	if err := root.Close(); err != nil {
		return "", err
	}

	return filepath.Clean(directory), nil
}
