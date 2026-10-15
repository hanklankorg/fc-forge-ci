// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	domainartifact "github.com/diggsweden/forge-ci/internal/domain/artifact"
	domainci "github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

const defaultReleaseImagesPath = "dist/release-images.json"

// AssembleDistInput drives `forge-ci release assemble-dist`, the Forgejo CI
// signer hand-off fan-in: download run artifacts, optionally merge image
// ledgers, prune directories on request, and emit the canonical dist digest.
type AssembleDistInput struct {
	ArtifactNames            string
	ArtifactTransferPlanJSON string
	Path                     string
	LedgerFiles              string
	LedgerExpectedCount      int
	ReleaseImagesPath        string
	PruneDirs                bool
	RunID                    string
	Repository               string
}

// AssembleDistResult is the machine-readable hand-off result.
type AssembleDistResult struct {
	Digest string
}

// distAcquirer hands over the dist directory a hand-off digests. The osfs
// adapter owns what "inside the workspace" means on disk and the pruning.
type distAcquirer interface {
	AcquireDist(path string, pruneDirs bool) (string, error)
}

// distWorkspace is the dist directory AssembleDist downloads into: contained
// before the first download, acquired after the last write.
type distWorkspace interface {
	distAcquirer
	ContainDistPath(path string) error
}

// AssembleDist performs the release dist/ hand-off assembly formerly owned by
// forgejo-ci's assemble-release-dist.sh.
func AssembleDist(ctx context.Context, dl provider.RunArtifactDownloader, workspace distWorkspace, sink domainci.OutputSink, stderr io.Writer, in AssembleDistInput) (AssembleDistResult, error) { //nolint:cyclop // linear hand-off pipeline: validate, download, merge, prune, digest.
	path := defaultIfEmpty(in.Path, "dist/")
	releaseImagesPath := defaultIfEmpty(in.ReleaseImagesPath, defaultReleaseImagesPath)

	if err := validateSingleLineValue(path, "path"); err != nil {
		return AssembleDistResult{}, err
	}

	if err := workspace.ContainDistPath(path); err != nil {
		return AssembleDistResult{}, err
	}

	if err := validateSafeRelativePath(releaseImagesPath, "release-images-path"); err != nil {
		return AssembleDistResult{}, err
	}

	// The ledger is a file the release publishes. "-" would send it to stdout
	// and report success with nothing on disk.
	if releaseImagesPath == cliio.StdSentinel {
		return AssembleDistResult{}, fmt.Errorf("release-images-path must be a file path, not %q: %w", cliio.StdSentinel, errs.ErrUsage)
	}

	if err := downloadAssembleArtifacts(ctx, dl, stderr, in.ArtifactNames, in.ArtifactTransferPlanJSON, path, in.RunID, in.Repository); err != nil {
		return AssembleDistResult{}, err
	}

	if err := mergeReleaseImageLedgers(stderr, in.LedgerFiles, releaseImagesPath, in.LedgerExpectedCount); err != nil {
		return AssembleDistResult{}, err
	}

	digestDir, err := workspace.AcquireDist(path, in.PruneDirs)
	if err != nil {
		return AssembleDistResult{}, err
	}

	digest, err := DistDigest(digestDir)
	if err != nil {
		return AssembleDistResult{}, err
	}

	if sink != nil {
		if err := sink.Set(ctx, "digest", digest); err != nil {
			return AssembleDistResult{}, err
		}
	}

	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "%s digest: %s\n", digestDir, digest)
	}

	return AssembleDistResult{Digest: digest}, nil
}

func downloadAssembleArtifacts(ctx context.Context, dl provider.RunArtifactDownloader, stderr io.Writer, artifactNames, planJSON, path, runID, repository string) error {
	if planJSON != "" {
		return downloadAssembleArtifactsFromPlan(ctx, dl, stderr, artifactNames, planJSON, runID, repository)
	}

	return downloadAssembleArtifactsFromNames(ctx, dl, stderr, artifactNames, path, runID, repository)
}

func downloadAssembleArtifactsFromNames(ctx context.Context, dl provider.RunArtifactDownloader, stderr io.Writer, artifactNames, path, runID, repository string) error {
	count := 0

	for _, name := range splitLinesPreserveValues(artifactNames) {
		if name == "" {
			continue
		}

		if err := downloadAssembleArtifact(ctx, dl, stderr, name, path, true, runID, repository); err != nil {
			return err
		}

		count++
	}

	if count == 0 {
		return fmt.Errorf("artifact-names must list at least one artifact when artifact-transfer-plan-json is empty: %w", errs.ErrUsage)
	}

	return nil
}

func downloadAssembleArtifactsFromPlan(ctx context.Context, dl provider.RunArtifactDownloader, stderr io.Writer, artifactNames, raw, runID, repository string) error {
	if compactArtifactNames(artifactNames) != "" {
		return fmt.Errorf("artifact-transfer-plan-json and artifact-names are mutually exclusive: %w", errs.ErrUsage)
	}

	// Validated whole before the first download, so a plan refused on its
	// third item has not already fetched the first two.
	plan, err := pipeline.ParseArtifactTransferPlan(raw)
	if err != nil {
		return err
	}

	for _, item := range plan.Items {
		if err := downloadAssemblePlanItem(ctx, dl, stderr, item, runID, repository); err != nil {
			return err
		}
	}

	if len(plan.Items) == 0 && stderr != nil {
		_, _ = fmt.Fprintln(stderr, "warning: artifact-transfer-plan-json contains no transfer items")
	}

	return nil
}

func downloadAssemblePlanItem(ctx context.Context, dl provider.RunArtifactDownloader, stderr io.Writer, item pipeline.ArtifactTransfer, runID, repository string) error {
	name, err := transferArtifactName(item, runID)
	if err != nil {
		return err
	}

	return downloadAssembleArtifact(ctx, dl, stderr, name, item.Path, item.Required, runID, repository)
}

func downloadAssembleArtifact(ctx context.Context, dl provider.RunArtifactDownloader, stderr io.Writer, name, dir string, required bool, runID, repository string) error {
	if err := domainartifact.ValidateName(name); err != nil {
		return err
	}

	if err := validateSingleLineValue(dir, "artifact path"); err != nil {
		return err
	}

	_, err := dl.DownloadRunArtifact(ctx, provider.RunArtifactDownload{
		Name:       name,
		Dir:        dir,
		RunID:      runID,
		Repository: repository,
	})
	if err == nil {
		if stderr != nil {
			_, _ = fmt.Fprintf(stderr, "Downloaded %s -> %s\n", name, dir)
		}

		return nil
	}

	if required {
		return err
	}

	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "warning: optional artifact '%s' was not downloaded: %v\n", name, err)
	}

	return nil
}

func mergeReleaseImageLedgers(out io.Writer, ledgerFiles, releaseImagesPath string, expectedCount int) error {
	if expectedCount < 0 {
		return fmt.Errorf("ledger-expected-count must be zero or greater: %w", errs.ErrUsage)
	}

	if ledgerFiles == "" {
		if expectedCount > 0 {
			return fmt.Errorf("ledger-expected-count requires ledger-files: %w", errs.ErrUsage)
		}

		return nil
	}

	files, err := collectReleaseLedgerFiles(out, ledgerFiles)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return writeEmptyReleaseLedger(out, releaseImagesPath, expectedCount)
	}

	return writeMergedReleaseLedger(out, files, releaseImagesPath, expectedCount)
}

func collectReleaseLedgerFiles(out io.Writer, ledgerFiles string) ([]string, error) {
	files := make([]string, 0)

	for _, file := range splitLinesPreserveValues(ledgerFiles) {
		if file == "" {
			continue
		}

		if err := validateSafeRelativePath(file, "ledger file"); err != nil {
			return nil, err
		}

		found, err := releaseLedgerInputFiles(file, out)
		if err != nil {
			return nil, err
		}

		files = append(files, found...)
	}

	return files, nil
}

func writeEmptyReleaseLedger(out io.Writer, releaseImagesPath string, expectedCount int) error {
	if expectedCount > 0 {
		return fmt.Errorf("release image ledger contains 0 images, expected %d: %w", expectedCount, errs.ErrValidation)
	}

	if err := writeReleaseLedger(releaseImagesPath, []byte("[]\n")); err != nil {
		return err
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "warning: no release image ledger entries found; wrote empty ledger to %s\n", releaseImagesPath)
	}

	return nil
}

func writeMergedReleaseLedger(out io.Writer, files []string, releaseImagesPath string, expectedCount int) error {
	merged, err := mergeLedgerJSONFiles(files)
	if err != nil {
		return err
	}

	if expectedCount > 0 && len(merged) != expectedCount {
		return fmt.Errorf("release image ledger contains %d images, expected %d: %w", len(merged), expectedCount, errs.ErrValidation)
	}

	body, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal release image ledger: %w", err)
	}

	if err := writeReleaseLedger(releaseImagesPath, append(body, '\n')); err != nil {
		return err
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "release image ledger contains %d images\n", len(merged))
	}

	return nil
}

// writeReleaseLedger creates the ledger's directory and writes the ledger. It
// runs only after the merge is accepted, so a refused merge leaves nothing behind.
func writeReleaseLedger(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec,mnd // public release metadata path under workspace.
		return fmt.Errorf("create release image ledger directory: %w", err)
	}

	if err := cliio.WriteFile(path, body, 0o644); err != nil { //nolint:mnd // public release metadata.
		return fmt.Errorf("write release image ledger %s: %w", path, err)
	}

	return nil
}

func releaseLedgerInputFiles(path string, out io.Writer) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if out != nil {
				_, _ = fmt.Fprintf(out, "warning: release image ledger input not found, skipping: %s\n", path)
			}

			return nil, nil
		}

		return nil, fmt.Errorf("stat release image ledger input %s: %w", path, err)
	}

	if info.IsDir() {
		return walkReleaseLedgerDir(path)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("release image ledger input must be a file or directory: %s: %w", path, errs.ErrValidation)
	}

	return []string{path}, nil
}

func walkReleaseLedgerDir(path string) ([]string, error) {
	var files []string

	err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() || filepath.Base(current) != "release-images.json" || !entry.Type().IsRegular() {
			return nil
		}

		files = append(files, current)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk release image ledger input %s: %w", path, err)
	}

	slices.Sort(files)

	return files, nil
}

func mergeLedgerJSONFiles(files []string) ([]any, error) {
	merged := make([]any, 0)

	for _, file := range files {
		entries, err := readLedgerJSONFile(file)
		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			if ledgerEntrySeen(merged, entry) {
				continue
			}

			merged = append(merged, entry)
		}
	}

	return merged, nil
}

func readLedgerJSONFile(path string) ([]any, error) {
	body, err := os.ReadFile(path) //nolint:gosec // workspace-local release metadata selected by caller.
	if err != nil {
		return nil, fmt.Errorf("read release image ledger %s: %w", path, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse release image ledger %s: %w: %w", path, err, errs.ErrInvalidConfig)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("parse release image ledger %s: trailing JSON value: %w", path, errs.ErrInvalidConfig)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse release image ledger %s: %w: %w", path, err, errs.ErrInvalidConfig)
	}

	entries, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("release image ledger must be a JSON array: %s: %w", path, errs.ErrInvalidConfig)
	}

	return entries, nil
}

func ledgerEntrySeen(entries []any, candidate any) bool {
	for _, entry := range entries {
		if reflect.DeepEqual(entry, candidate) {
			return true
		}
	}

	return false
}

func validateSingleLineValue(value, label string) error {
	if value == "" || strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("%s must be a non-empty single-line value: %w", label, errs.ErrUsage)
	}

	return nil
}

// validateSafeRelativePath wraps the shared predicate in this package's error
// shape. The rule itself lives in pathsafe so the two layers that need it
// cannot drift apart again.
func validateSafeRelativePath(path, label string) error {
	if !pathsafe.Relative(path) {
		return fmt.Errorf("%s must be a safe relative path: %s: %w", label, path, errs.ErrUsage)
	}

	return nil
}

func splitLinesPreserveValues(raw string) []string {
	if raw == "" {
		return nil
	}

	return strings.Split(raw, "\n")
}

func compactArtifactNames(raw string) string {
	withoutLF := strings.ReplaceAll(raw, "\n", "")

	return strings.ReplaceAll(withoutLF, "\r", "")
}

func defaultIfEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}
