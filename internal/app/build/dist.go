// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"errors"
	"fmt"
	"os"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// openPlatformDist binds Go/Cargo cleanup to the actual dist child. The general
// root opener admits root-owned OS layout links; dist is application output and
// must not be a link even when the build runs as root in a CI container.
func openPlatformDist(dir string) (*os.Root, string, error) {
	project, err := pathsafe.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}

	defer func() { _ = project.Close() }()

	if err = project.Mkdir("dist", 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, "", err
	}

	info, err := project.Lstat("dist")
	if err != nil {
		return nil, "", err
	}

	if !info.IsDir() {
		return nil, "", fmt.Errorf("dist must be a real directory: %w", errs.ErrValidation)
	}

	dist, err := project.OpenRoot("dist")

	return dist, project.Name(), err
}
