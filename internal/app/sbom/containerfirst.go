// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// ContainerFirstBuildSBOMsInput drives `sbom assemble --artifacts-config`.
type ContainerFirstBuildSBOMsInput struct {
	// Artifacts are the declaration's container-first artifacts whose build
	// SBOM forge-ci writes (pipeline.ArtifactSets.ContainerFirstBuildSBOMs).
	Artifacts []pipeline.PlannedArtifact
	// ProjectType selects the artifacts of the job's toolchain, go or cargo.
	ProjectType projecttype.Type
	Version     string
	// OutputDir receives each build SBOM under its release name.
	OutputDir string
}

// ContainerFirstBuildSBOMs writes the build SBOM of every container-first
// artifact of one project type from its lockfile, as GitHub's sbom-go.yml
// and sbom-cargo.yml do, and moves it into OutputDir under the name the
// build layer has on every forge: <artifact>-<version>[-<sha>]-build-sbom.
// cyclonedx.json, the version without its v. An artifact of the type is never skipped: the declaration
// plans its SBOM, and the release refuses a bundle without it.
func ContainerFirstBuildSBOMs(ctx context.Context, gitRepo GitOps, gen BuildSBOMGenerator, out, stderr io.Writer, in ContainerFirstBuildSBOMsInput) ([]string, error) {
	if in.ProjectType != projecttype.Go && in.ProjectType != projecttype.Cargo {
		return nil, fmt.Errorf("container-first build SBOMs are written for go or cargo, not %q: %w", in.ProjectType, errs.ErrUsage)
	}

	if strings.TrimSpace(in.Version) == "" || strings.TrimSpace(in.OutputDir) == "" {
		return nil, fmt.Errorf("container-first build SBOMs need --version and --output-dir: %w", errs.ErrUsage)
	}

	if err := os.MkdirAll(in.OutputDir, 0o755); err != nil { //nolint:gosec // release files under the workspace.
		return nil, fmt.Errorf("create %s: %w", in.OutputDir, err)
	}

	var written []string

	for _, artifact := range in.Artifacts {
		if artifact.ProjectType != in.ProjectType {
			continue
		}

		outputs := map[string]bool{}
		if err := Generate(ctx, nil, nil, gitRepo, gen, out, stderr, GenerateInput{
			ProjectType:      string(artifact.ProjectType),
			Layers:           "build",
			Version:          version.StripVPrefix(strings.TrimSpace(in.Version)),
			Name:             artifact.Name,
			WorkingDir:       dirOrDot(artifact.WorkingDirectory),
			generatedOutputs: outputs,
		}); err != nil {
			return nil, fmt.Errorf("build SBOM of %s: %w", artifact.Name, err)
		}

		for _, path := range slices.Sorted(maps.Keys(outputs)) {
			target := filepath.Join(in.OutputDir, filepath.Base(path))
			if err := moveAcrossDevices(path, target); err != nil {
				return nil, fmt.Errorf("move build SBOM of %s: %w", artifact.Name, err)
			}

			written = append(written, target)
		}
	}

	return written, nil
}

// moveAcrossDevices moves src to dst, which must not exist. It copies: on a
// GitLab runner the checkout and the hand-off can be different filesystems,
// where a rename fails.
func moveAcrossDevices(src, dst string) error {
	body, err := os.ReadFile(src) //nolint:gosec // src is a file the generator wrote
	if err != nil {
		return err
	}

	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // dst is under the caller's output dir
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s already exists: %w", dst, errs.ErrValidation)
		}

		return err
	}

	if _, err := file.Write(body); err != nil {
		_ = file.Close()

		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	return os.Remove(src)
}

func dirOrDot(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return "."
	}

	return dir
}
