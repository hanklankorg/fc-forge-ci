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
func ContainerFirstBuildSBOMs(ctx context.Context, gitRepo GitOps, gen BuildSBOMGenerator, out, stderr io.Writer, in ContainerFirstBuildSBOMsInput) (_ []string, err error) {
	if in.ProjectType != projecttype.Go && in.ProjectType != projecttype.Cargo {
		return nil, fmt.Errorf("container-first build SBOMs are written for go or cargo, not %q: %w", in.ProjectType, errs.ErrUsage)
	}

	if strings.TrimSpace(in.Version) == "" || strings.TrimSpace(in.OutputDir) == "" {
		return nil, fmt.Errorf("container-first build SBOMs need --version and --output-dir: %w", errs.ErrUsage)
	}

	if err := os.MkdirAll(in.OutputDir, 0o755); err != nil { //nolint:gosec // release files under the workspace.
		return nil, fmt.Errorf("create %s: %w", in.OutputDir, err)
	}

	// The SBOMs come from the lockfile alone: a bom.json already under an
	// artifact's directory, committed or left by a build step, would be
	// harvested in its place. The generator's own, and the ones it writes
	// beside every member of a Cargo workspace, go once all are written.
	before, err := buildBOMs(".")
	if err != nil {
		return nil, err
	}

	defer func() {
		if cleanErr := removeNewBuildBOMs(before); cleanErr != nil && err == nil {
			err = fmt.Errorf("remove the generated BOMs: %w", cleanErr)
		}
	}()

	written := []string{}

	for _, artifact := range in.Artifacts {
		if artifact.ProjectType != in.ProjectType {
			continue
		}

		dir := dirOrDot(artifact.WorkingDirectory)
		if bom := harvestableBOM(before, dir); bom != "" {
			return nil, fmt.Errorf("%s would stand in for the build SBOM of %s, which forge-ci writes from the lockfile; remove it, or skip the build step's own build SBOM: %w", bom, artifact.Name, errs.ErrValidation)
		}

		outputs := map[string]bool{}
		if err := Generate(ctx, nil, nil, gitRepo, gen, out, stderr, GenerateInput{
			ProjectType:      string(artifact.ProjectType),
			Layers:           "build",
			Version:          version.StripVPrefix(strings.TrimSpace(in.Version)),
			Name:             artifact.Name,
			WorkingDir:       dir,
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

// checkoutState are the bom.json files and the directories under the
// checkout, outside .git and the directories no harvest reads, dist/ and
// Cargo's target/.
type checkoutState struct{ boms, dirs map[string]bool }

func buildBOMs(root string) (checkoutState, error) {
	found := checkoutState{boms: map[string]bool{}, dirs: map[string]bool{}}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && path != root && slices.Contains([]string{".git", "dist", "target"}, entry.Name()) {
			return filepath.SkipDir
		}

		switch {
		case entry.IsDir():
			found.dirs[filepath.Clean(path)] = true
		case entry.Name() == "bom.json":
			found.boms[filepath.Clean(path)] = true
		}

		return nil
	})

	return found, err
}

// harvestableBOM is a bom.json of found under dir, empty when none is.
func harvestableBOM(found checkoutState, dir string) string {
	prefix := filepath.Clean(dir) + string(filepath.Separator)

	for _, path := range slices.Sorted(maps.Keys(found.boms)) {
		if dir == "." || strings.HasPrefix(path, prefix) {
			return path
		}
	}

	return ""
}

// removeNewBuildBOMs removes the bom.json files that were not there before,
// and the directories made for them, so the checkout is left as it was.
func removeNewBuildBOMs(before checkoutState) error {
	after, err := buildBOMs(".")
	if err != nil {
		return err
	}

	for _, path := range slices.Sorted(maps.Keys(after.boms)) {
		if before.boms[path] {
			continue
		}

		if err := os.Remove(path); err != nil {
			return err
		}

		for dir := filepath.Dir(path); dir != "." && !before.dirs[dir]; dir = filepath.Dir(dir) {
			if os.Remove(dir) != nil {
				break // not empty
			}
		}
	}

	return nil
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
