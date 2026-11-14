// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ExtractNPMTarballInput drives ExtractNPMTarball.
type ExtractNPMTarballInput struct {
	// Dir is the directory containing the tarball. Empty → cwd.
	Dir string
}

// ExtractNPMTarball finds a *.tgz / *.tar.gz at the top of Dir,
// extracts it with `tar --strip-components=1` semantics and removes the
// tarball for container build contexts. No tarball is an error: the image job
// runs this for a container that names an npm artifact, and the packed package
// is that artifact. Which directories it holds is the package's own choice,
// so the package arriving is the check.
func ExtractNPMTarball(out io.Writer, in ExtractNPMTarballInput) error {
	dir := in.Dir
	if dir == "" {
		var err error

		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}

	tarball, err := findFirstTarball(dir)
	if err != nil {
		return err
	}

	if tarball == "" {
		return fmt.Errorf("no npm package (*.tgz) in %s: the build job's package did not arrive: %w", dir, errs.ErrMissingInput)
	}

	_, _ = fmt.Fprintf(out, "Extracting %s...\n", tarball)

	if err := archive.UntarStripOne(tarball, dir); err != nil {
		return fmt.Errorf("extract %s: %w", tarball, err)
	}

	if err := os.Remove(tarball); err != nil {
		return fmt.Errorf("remove tarball: %w", err)
	}

	return nil
}

func findFirstTarball(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", dir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		name := e.Name()
		if strings.HasSuffix(name, ".tgz") || strings.HasSuffix(name, ".tar.gz") {
			return filepath.Join(dir, name), nil
		}
	}

	return "", nil
}
