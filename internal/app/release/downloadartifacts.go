// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/clicolor"
	domainci "github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// DownloadArtifactsInput drives DownloadArtifacts.
type DownloadArtifactsInput struct {
	ArtifactTransferPlanJSON string
	RunID                    string
	Repository               string
}

// DownloadArtifacts downloads release artifacts from an explicit transfer plan.
func DownloadArtifacts(ctx context.Context, dl provider.RunArtifactDownloader, stderr io.Writer, in DownloadArtifactsInput) error {
	return downloadArtifacts(ctx, dl, stderr, in, "")
}

// AcquireArtifacts gives this job a fresh producer namespace, never a directory
// from the source checkout. The runner owns successful acquisitions until job
// cleanup; failures remove only the private directory created by this call.
func AcquireArtifacts(ctx context.Context, dl provider.RunArtifactDownloader, sink domainci.OutputSink, stderr io.Writer, in DownloadArtifactsInput) (string, error) {
	_, _, err := validatedArtifactDownloads(in)
	if err != nil {
		return "", err
	}

	base, err := filepath.Abs(runcontext.TempRoot(os.Getenv))
	if err != nil {
		return "", err
	}

	contained, err := pathsafe.RootContains(".", base)
	if err != nil {
		return "", err
	}

	if contained {
		return "", fmt.Errorf("acquisition parent must not be inside the source checkout: %w", errs.ErrValidation)
	}

	root, err := pathsafe.OpenRoot(base)
	if err != nil {
		return "", err
	}

	defer func() { _ = root.Close() }()

	dir, err := os.MkdirTemp(base, "forge-ci-release-acquisition-*")
	if err != nil {
		return "", err
	}

	success := false
	defer func() {
		if !success {
			_ = root.RemoveAll(filepath.Base(dir))
		}
	}()

	if err := validateAcquisitionRoot(dir); err != nil {
		return "", err
	}

	// All paths and resolved names were validated before creating the directory.
	if err := downloadArtifacts(ctx, dl, stderr, in, dir); err != nil {
		return "", err
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	if sink != nil {
		if err := sink.Set(ctx, "acquisition-dir", dir); err != nil {
			return "", err
		}
	}

	success = true

	return dir, nil
}

// validateAcquisitionRoot requires the final acquisition tree and checkout to
// be disjoint in both directions. Temp parents have a separate, one-way rule.
func validateAcquisitionRoot(dir string) error {
	disjoint, err := pathsafe.DisjointRoots(".", dir)
	if err != nil {
		return err
	}

	if !disjoint {
		return fmt.Errorf("acquisition directory must be disjoint from the source checkout: %w", errs.ErrValidation)
	}

	return nil
}

func validatedArtifactDownloads(in DownloadArtifactsInput) (pipeline.ArtifactTransferPlan, []string, error) {
	if strings.TrimSpace(in.ArtifactTransferPlanJSON) == "" {
		return pipeline.ArtifactTransferPlan{}, nil, fmt.Errorf("artifact-transfer-plan-json is required: %w", errs.ErrUsage)
	}

	// The whole plan is validated before anything is fetched, so a refused
	// plan leaves the workspace as it found it.
	plan, err := pipeline.ParseArtifactTransferPlan(in.ArtifactTransferPlanJSON)
	if err != nil {
		return pipeline.ArtifactTransferPlan{}, nil, err
	}

	names := make([]string, len(plan.Items))
	for index, item := range plan.Items {
		name, err := transferArtifactName(item, in.RunID)
		if err != nil {
			return pipeline.ArtifactTransferPlan{}, nil, err
		}

		names[index] = name
	}

	return plan, names, nil
}

func downloadArtifacts(ctx context.Context, dl provider.RunArtifactDownloader, stderr io.Writer, in DownloadArtifactsInput, acquisitionDir string) error {
	plan, names, err := validatedArtifactDownloads(in)
	if err != nil {
		return err
	}

	for index, item := range plan.Items {
		name := names[index]

		dir := item.Path
		if acquisitionDir != "" {
			dir = filepath.Join(acquisitionDir, item.Path)
		}

		request := provider.RunArtifactDownload{
			RunID:      in.RunID,
			Repository: in.Repository,
			Name:       name,
			Dir:        dir,
		}

		var err error
		if acquisitionDir != "" {
			err = downloadAcquiredArtifact(ctx, dl, request, item.Required)
		} else {
			_, err = dl.DownloadRunArtifact(ctx, request)
		}

		if err == nil {
			if stderr != nil {
				_, _ = fmt.Fprintf(stderr, "%s Downloaded %s → %s\n", clicolor.Check(stderr), name, dir)
			}

			continue
		}

		if item.Required {
			return err
		}

		if stderr != nil {
			_, _ = fmt.Fprintf(stderr, "warning: optional artifact %q was not downloaded: %v\n", name, err)
		}
	}

	return nil
}

// Each transfer gets fresh native staging so an earlier transfer's files cannot
// satisfy this transfer's required-content check, even with overlapping paths.
func downloadAcquiredArtifact(ctx context.Context, dl provider.RunArtifactDownloader, request provider.RunArtifactDownload, required bool) error {
	stage, err := pathsafe.NewArtifactStaging(request.Dir)
	if err != nil {
		return err
	}
	defer func() { _ = stage.Close() }()

	request.Dir = stage.Root().Name()

	observation, err := dl.DownloadRunArtifact(ctx, request)
	if err != nil {
		return err
	}

	if required {
		if err := requireAcquiredContent(stage, observation); err != nil {
			return err
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	return stage.Install()
}

// Provider counts can include directory entries; independently open/read actual
// regular members before installing them. Empty regular files remain legitimate.
func requireAcquiredContent(stage *pathsafe.ArtifactStaging, observation provider.RunArtifactInfo) error {
	files := 0

	err := fs.WalkDir(stage.Root().FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		file, err := openReleaseFile(filepath.Join(stage.Root().Name(), filepath.FromSlash(path)))
		if err != nil {
			return err
		}

		_, readErr := io.Copy(io.Discard, file)
		closeErr := file.Close()

		if readErr != nil {
			return readErr
		}

		if closeErr != nil {
			return closeErr
		}

		files++

		return nil
	})
	if err != nil {
		return err
	}

	if files == 0 {
		return fmt.Errorf("required artifact %q has no acquired regular files (provider reported %d members): %w", observation.Name, observation.FileCount, errs.ErrMissingInput)
	}

	return nil
}

func transferArtifactName(item pipeline.ArtifactTransfer, runID string) (string, error) {
	if item.Name != "" {
		return item.Name, nil
	}

	if strings.Contains(item.NameTemplate, "{run_id}") && runID == "" {
		return "", fmt.Errorf("run-id is required for artifact transfer %q: %w", item.NameTemplate, errs.ErrUsage)
	}

	name := strings.ReplaceAll(item.NameTemplate, "{run_id}", runID)
	if strings.ContainsAny(name, "{}") {
		return "", fmt.Errorf("artifact transfer name template %q contains unresolved placeholders: %w", item.NameTemplate, errs.ErrInvalidConfig)
	}

	return name, nil
}
