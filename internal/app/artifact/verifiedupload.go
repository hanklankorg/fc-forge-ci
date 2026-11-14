// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"

	domainartifact "github.com/diggsweden/forge-ci/internal/domain/artifact"
	domainci "github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// UploadVerified uploads once and reads back once before emitting success.
// Neither an uncertain mutation nor a failed read-back is retried here. The
// read-back lands under scratch, the runner's temp directory, which can hold a
// copy of the artifact where the system temp may be a small tmpfs; empty
// falls back to the system temp.
func UploadVerified(ctx context.Context, up provider.RunArtifactUploader, dl provider.RunArtifactDownloader, sink domainci.OutputSink, out io.Writer, in provider.RunArtifactUpload, scratch string) (provider.RunArtifactInfo, error) {
	if in.IfNoFiles != "" && in.IfNoFiles != provider.IfNoFilesError {
		return provider.RunArtifactInfo{}, fmt.Errorf("verified upload requires --if-no-files error: %w", errs.ErrUsage)
	}

	entries, err := domainartifact.CollectUpload(in.Dir, in.Files, in.Paths, in.IncludeHidden)
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}

	if len(entries) == 0 {
		return provider.RunArtifactInfo{}, fmt.Errorf("verified upload has no files: %w", errs.ErrValidation)
	}

	expected, err := uploadInventory(ctx, entries)
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}

	work, err := os.MkdirTemp(scratch, "forge-ci-artifact-readback-*")
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}
	defer func() { _ = os.RemoveAll(work) }()

	info, err := Upload(ctx, up, nil, nil, in)
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}

	dir := filepath.Join(work, "download")
	if _, err := dl.DownloadRunArtifact(ctx, provider.RunArtifactDownload{Name: in.Name, Dir: dir}); err != nil {
		return provider.RunArtifactInfo{}, fmt.Errorf("read back artifact %s: %w", in.Name, err)
	}
	// Include hidden returned files: even a hidden extra invalidates the handoff.
	returned, err := domainartifact.CollectUpload(dir, nil, nil, true)
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}

	actual, err := uploadInventory(ctx, returned)
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}

	if !maps.Equal(expected, actual) {
		return provider.RunArtifactInfo{}, fmt.Errorf("artifact %s read-back has missing, changed or extra files: %w", in.Name, errs.ErrValidation)
	}

	if err := emit(ctx, sink, out, info, "Uploaded and verified", ""); err != nil {
		return provider.RunArtifactInfo{}, err
	}

	return info, nil
}

func uploadInventory(ctx context.Context, entries []domainartifact.UploadEntry) (map[string]string, error) {
	if len(entries) > domainartifact.MaxFileCount {
		return nil, fmt.Errorf("artifact inventory exceeds file limit: %w", errs.ErrValidation)
	}

	result := make(map[string]string, len(entries))

	var total int64

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		file, err := domainartifact.OpenUploadEntry(entry)
		if err != nil {
			return nil, err
		}

		hash := sha256.New()
		n, copyErr := domainartifact.CopyAtMost(hash, file, domainartifact.MaxFileBytes)
		closeErr := file.Close()

		if copyErr != nil {
			return nil, copyErr
		}

		if closeErr != nil {
			return nil, closeErr
		}

		total += n
		if total > domainartifact.MaxTotalBytes {
			return nil, fmt.Errorf("artifact inventory exceeds byte limit: %w", errs.ErrValidation)
		}

		result[entry.RelPath] = hex.EncodeToString(hash.Sum(nil))
	}

	return result, nil
}
