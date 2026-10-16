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
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	domainversion "github.com/diggsweden/forge-ci/internal/domain/version"
)

// applyBumpOps is the git surface ApplyBump needs: a dry and a real apply.
type applyBumpOps interface {
	ApplyPatch(ctx context.Context, patchPath string, checkOnly bool) error
}

// ApplyBumpInput drives `forge-ci version apply-bump`.
type ApplyBumpInput struct {
	PatchPath   string           // patch the key-free bump job uploaded
	SHA256      string           // digest that job reported, lowercase hex
	ProjectType projecttype.Type // ecosystem whose project files the patch may touch
	Version     string           // release version, with or without a leading v
	WorkingDir  string           // repository-relative directory of the root project file
	PathsFile   string           // where the changed paths are written, one per line
}

// ApplyBump applies a version bump made in another job, after proving it is
// one. The patch must match the digest its job reported, change only the
// project files of the type, and change nothing but the root project's current
// version into the release version. Then git applies it, the root project file
// is read again, and the changed paths are written for the release commit.
//
// It runs no build tool: the job that holds the signing key only checks text.
//
//nolint:cyclop // flat input validation, then the ordered proof and apply steps.
func ApplyBump(ctx context.Context, ops applyBumpOps, out io.Writer, in ApplyBumpInput) (domainversion.BumpPatchResult, error) {
	var none domainversion.BumpPatchResult

	files, supported := domainversion.BumpPatchFiles(in.ProjectType)
	release := strings.TrimPrefix(in.Version, "v")
	workingDir := path.Clean(in.WorkingDir)

	switch {
	case !supported:
		return none, fmt.Errorf("apply-bump: project type %q has no isolated version bump: %w", in.ProjectType, errs.ErrUsage)
	case !domainversion.IsStableSemverTag("v" + release):
		return none, fmt.Errorf("apply-bump: version %q is not a stable MAJOR.MINOR.PATCH: %w", in.Version, errs.ErrValidation)
	case !container.ValidSHA256Hex(in.SHA256):
		return none, fmt.Errorf("apply-bump: sha256 must be 64 lowercase hex characters: %w", errs.ErrUsage)
	case in.PatchPath == "" || in.PathsFile == "":
		return none, fmt.Errorf("apply-bump: patch and paths file are required: %w", errs.ErrUsage)
	case path.IsAbs(workingDir) || workingDir == ".." || strings.HasPrefix(workingDir, "../"):
		return none, fmt.Errorf("apply-bump: working directory %q must be repository-relative: %w", in.WorkingDir, errs.ErrUsage)
	}

	patch, err := cliio.ReadRegularFile(in.PatchPath)
	if err != nil {
		return none, fmt.Errorf("apply-bump: read patch: %w", err)
	}

	sum := sha256.Sum256(patch)
	if hex.EncodeToString(sum[:]) != in.SHA256 {
		return none, fmt.Errorf("apply-bump: the patch does not match the digest its job reported: %w", errs.ErrValidation)
	}

	result, err := domainversion.VerifyBumpPatch(patch, files, release)
	if err != nil {
		return none, err
	}

	root := path.Join(workingDir, files[0])
	if !slices.Contains(result.Paths, root) {
		return none, fmt.Errorf("apply-bump: the patch leaves the root project file %s unchanged: %w", root, errs.ErrValidation)
	}

	if err := requireProjectVersion(root, result.OldVersion); err != nil {
		return none, fmt.Errorf("apply-bump: before the bump: %w", err)
	}

	if err := ops.ApplyPatch(ctx, in.PatchPath, true); err != nil {
		return none, fmt.Errorf("apply-bump: the patch does not apply to this checkout: %w", err)
	}

	if err := ops.ApplyPatch(ctx, in.PatchPath, false); err != nil {
		return none, fmt.Errorf("apply-bump: apply: %w", err)
	}

	if err := requireProjectVersion(root, release); err != nil {
		return none, fmt.Errorf("apply-bump: after the bump: %w", err)
	}

	if err := os.WriteFile(in.PathsFile, []byte(strings.Join(result.Paths, "\n")+"\n"), 0o600); err != nil {
		return none, fmt.Errorf("apply-bump: write paths file: %w", err)
	}

	_, _ = fmt.Fprintf(out, "Version bump %s -> %s verified and applied: %s\n", result.OldVersion, release, strings.Join(result.Paths, ", "))

	return result, nil
}

// requireProjectVersion reads the root project file inside the checkout, never
// through a link out of it, and compares its declared version.
func requireProjectVersion(file, want string) error {
	root, err := os.OpenRoot(".")
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	body, err := cliio.ReadFileInRoot(root, file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}

	pom, err := build.ParsePOM(body)
	if err != nil {
		return fmt.Errorf("parse %s: %w", file, err)
	}

	if pom.Version != want {
		return fmt.Errorf("%s declares version %q, want %q: %w", file, pom.Version, want, errs.ErrValidation)
	}

	return nil
}
