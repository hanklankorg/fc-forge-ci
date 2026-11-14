// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	domainversion "github.com/diggsweden/forge-ci/internal/domain/version"
)

func nativeBump(kind projecttype.Type) bool {
	_, supported := domainversion.BumpPatchFiles(kind)

	return supported
}

// The trusted plan assigns each native file to a project; the existing
// ApplyBump verifier then proves each project's version-only substitution.
// Gradle/Xcode changes remain engine-owned text edits, and changelogs come
// from their separate data artifact, never from the native-tool patch. The
// patch may join several key-free jobs' patches, so a file it changes twice
// is refused. It returns the paths the patch changed.
func applyPlannedNativeBumps(ctx context.Context, git applyBumpOps, inputs []bumpTarget, in BumpPlanInput, out io.Writer) ([]string, error) {
	body, err := cliio.ReadRegularFile(in.PatchPath)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != in.PatchSHA256 {
		return nil, fmt.Errorf("version-bump plan patch differs from its key-free job's digest: %w", errs.ErrValidation)
	}

	version := strings.TrimPrefix(strings.TrimSpace(in.Version), "v")

	sections, err := domainversion.SplitBumpPatch(body, version)
	if err != nil {
		return nil, err
	}

	patches := make([][]byte, len(inputs))
	seen := make(map[string]bool, len(sections))

	for _, section := range sections {
		if seen[section.Path] {
			return nil, fmt.Errorf("version-bump patch changes %s twice: %w", section.Path, errs.ErrValidation)
		}

		seen[section.Path] = true

		index := plannedPatchTarget(inputs, section.Path)
		if index < 0 {
			return nil, fmt.Errorf("version-bump patch changes an unplanned project file %s: %w", section.Path, errs.ErrValidation)
		}

		patches[index] = append(patches[index], section.Patch...)
	}

	policy := in.ReleasePolicy
	if policy == "" {
		policy = "stable"
	}

	var applied []string

	for index, target := range inputs {
		files, supported := domainversion.BumpPatchFiles(target.ProjectType)
		if !supported {
			continue
		}

		if len(patches[index]) == 0 {
			if err := requireProjectVersion(path.Join(target.WorkingDir, files[0]), version); err != nil {
				return nil, err
			}

			continue
		}

		if git == nil {
			return nil, fmt.Errorf("prepared version bump requires a Git adapter: %w", errs.ErrUsage)
		}

		paths, err := applyProjectPatch(ctx, git, target, policy, version, patches[index], out)
		if err != nil {
			return nil, err
		}

		applied = append(applied, paths...)
	}

	return applied, nil
}

func plannedPatchTarget(inputs []bumpTarget, file string) int {
	scopes := make([]domainversion.BumpScope, len(inputs))
	for index, target := range inputs {
		scopes[index] = domainversion.BumpScope{ProjectType: target.ProjectType, WorkingDir: target.WorkingDir}
	}

	return domainversion.PlannedBumpScope(scopes, file)
}

func applyProjectPatch(ctx context.Context, git applyBumpOps, target bumpTarget, policy, version string, patch []byte, out io.Writer) ([]string, error) {
	file, err := os.CreateTemp("", "forge-ci-version-bump-*.patch")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(file.Name()) }()

	_, writeErr := file.Write(patch)
	closeErr := file.Close()

	if writeErr != nil {
		return nil, writeErr
	}

	if closeErr != nil {
		return nil, closeErr
	}

	paths, err := os.CreateTemp("", "forge-ci-version-paths-*")
	if err != nil {
		return nil, err
	}

	defer func() { _ = os.Remove(paths.Name()) }()

	if err := paths.Close(); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(patch)

	result, err := ApplyBump(ctx, git, out, ApplyBumpInput{ReleasePolicy: policy, PatchPath: file.Name(), SHA256: hex.EncodeToString(sum[:]),
		ProjectType: target.ProjectType, Version: version, WorkingDir: target.WorkingDir, PathsFile: paths.Name()})
	if err != nil {
		return nil, err
	}

	return result.Paths, nil
}
