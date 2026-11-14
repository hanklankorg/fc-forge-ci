// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	domainartifact "github.com/diggsweden/forge-ci/internal/domain/artifact"
	domainci "github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// BlobStoreUpload is an upload whose OCI layout blobs go to the run's blob
// store instead of into the artifact itself.
type BlobStoreUpload struct {
	provider.RunArtifactUpload

	// Prefix names the store: blob artifacts are <prefix>-<digest>-<owner>.
	Prefix string
	// MinBlobBytes is the smallest blob moved to the store; zero takes
	// domainartifact.MinSharedBlobBytes.
	MinBlobBytes int64
}

// BlobStoreDownload is a download that restores the blobs its artifact left
// in the run's blob store.
type BlobStoreDownload struct {
	provider.RunArtifactDownload

	// Prefix names the store the upload used.
	Prefix string
}

type blobUploadRoles interface {
	provider.RunArtifactUploader
	provider.RunArtifactLister
	// RunArtifactDownloader checks a copy this upload's name already holds
	// from an earlier attempt of the job before it is trusted.
	provider.RunArtifactDownloader
}

type blobDownloadRoles interface {
	provider.RunArtifactDownloader
	provider.RunArtifactLister
}

// UploadWithBlobStore uploads in.Dir as the artifact in.Name, except that
// every OCI layout blob of at least MinBlobBytes goes to the blob store as
// its own artifact, and only when no artifact of the run stores that digest
// yet. The artifact itself carries the remaining files and a manifest of the
// blobs, so DownloadWithBlobStore restores the directory exactly.
//
// Concurrent jobs may both store one blob, under two names; that costs
// bytes, never correctness, because the download verifies every blob it
// takes. A store upload that fails is still success when another upload of
// the same digest has appeared meanwhile.
func UploadWithBlobStore(ctx context.Context, store blobUploadRoles, sink domainci.OutputSink, out io.Writer, in BlobStoreUpload) (provider.RunArtifactInfo, error) {
	if err := validateBlobStoreUpload(in); err != nil {
		return provider.RunArtifactInfo{}, err
	}

	minBytes := in.MinBlobBytes
	if minBytes <= 0 {
		minBytes = domainartifact.MinSharedBlobBytes
	}

	entries, err := domainartifact.CollectUpload(in.Dir, nil, nil, in.IncludeHidden)
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}

	if len(entries) == 0 {
		// Nothing to split; the plain upload applies the IfNoFiles policy.
		return Upload(ctx, store, sink, out, in.RunArtifactUpload)
	}

	// Beside the directory, in the workspace: a kaniko job's own filesystem
	// does not survive the build, the workspace does.
	stage, err := os.MkdirTemp(filepath.Dir(filepath.Clean(in.Dir)), ".forge-ci-blob-upload-")
	if err != nil {
		return provider.RunArtifactInfo{}, fmt.Errorf("create upload staging: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	manifest, blobs, err := stageWithoutBlobs(entries, stage, minBytes)
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}

	var stored, reused int64

	for _, blob := range blobs {
		uploaded, storeErr := storeBlob(ctx, store, in, blob)
		if storeErr != nil {
			return provider.RunArtifactInfo{}, storeErr
		}

		if uploaded {
			stored += blob.Size
		} else {
			reused += blob.Size
		}
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "Blob store %s: %d blobs, %d bytes uploaded, %d bytes already in the run\n",
			in.Prefix, len(manifest.Blobs), stored, reused)
	}

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return provider.RunArtifactInfo{}, fmt.Errorf("encode blob manifest: %w", err)
	}

	if err := os.WriteFile(filepath.Join(stage, domainartifact.BlobManifestName), append(body, '\n'), 0o600); err != nil {
		return provider.RunArtifactInfo{}, fmt.Errorf("write blob manifest: %w", err)
	}

	layout := in.RunArtifactUpload
	layout.Dir = stage
	// The stage holds exactly the selected files; selection already applied
	// the caller's hidden-file choice.
	layout.IncludeHidden = true

	return Upload(ctx, store, sink, out, layout)
}

func validateBlobStoreUpload(in BlobStoreUpload) error {
	if err := domainartifact.ValidateBlobPrefix(in.Prefix); err != nil {
		return err
	}

	if in.Dir == "" || len(in.Files) > 0 || len(in.Paths) > 0 {
		return fmt.Errorf("a blob store upload takes --dir, not --file or --path: %w", errs.ErrUsage)
	}

	return domainartifact.ValidateName(in.Name)
}

// localBlob is one blob of the uploaded directory headed for the store.
type localBlob struct {
	domainartifact.BlobRef

	abs string
}

// stageWithoutBlobs copies every entry except the shared blobs into stage,
// verifies each blob against the digest its path names, and returns the
// manifest and the distinct blobs to store.
func stageWithoutBlobs(entries []domainartifact.UploadEntry, stage string, minBytes int64) (domainartifact.BlobManifest, []localBlob, error) {
	manifest := domainartifact.BlobManifest{Schema: domainartifact.BlobManifestSchema, Blobs: []domainartifact.BlobRef{}}

	var blobs []localBlob

	stored := map[string]bool{}

	for _, entry := range entries {
		if entry.RelPath == domainartifact.BlobManifestName {
			return manifest, nil, fmt.Errorf("%s is reserved for the blob manifest: %w", domainartifact.BlobManifestName, errs.ErrValidation)
		}

		digestHex, isBlob := domainartifact.SharedBlobHex(entry.RelPath)
		if !isBlob || entry.Size < minBytes {
			if err := copyEntry(entry, stage); err != nil {
				return manifest, nil, err
			}

			continue
		}

		if err := verifyFileDigest(entry, digestHex); err != nil {
			return manifest, nil, err
		}

		ref := domainartifact.BlobRef{Path: entry.RelPath, Digest: "sha256:" + digestHex, Size: entry.Size}
		manifest.Blobs = append(manifest.Blobs, ref)

		if !stored[digestHex] {
			stored[digestHex] = true

			blobs = append(blobs, localBlob{BlobRef: ref, abs: entry.Abs})
		}
	}

	return manifest, blobs, nil
}

func copyEntry(entry domainartifact.UploadEntry, stage string) error {
	src, err := domainartifact.OpenUploadEntry(entry)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	dest := filepath.Join(stage, filepath.FromSlash(entry.RelPath))
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return fmt.Errorf("stage %q: %w", entry.RelPath, err)
	}

	dst, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // a collected relative path inside the private stage.
	if err != nil {
		return fmt.Errorf("stage %q: %w", entry.RelPath, err)
	}

	_, copyErr := io.Copy(dst, src)
	if closeErr := dst.Close(); copyErr == nil {
		copyErr = closeErr
	}

	if copyErr != nil {
		return fmt.Errorf("stage %q: %w", entry.RelPath, copyErr)
	}

	return nil
}

// storeBlob uploads one blob unless another uploader in the run already
// stores its digest, and reports whether it uploaded.
//
// This upload's own name never counts as stored on sight: the forge may list
// it from its first file on, so after a failed attempt of the job it may hold
// part of the blob. A re-run uploads it again, and when the store refuses
// that, keeps the earlier copy only once it is downloaded and verified.
func storeBlob(ctx context.Context, store blobUploadRoles, in BlobStoreUpload, blob localBlob) (bool, error) {
	names, err := store.ListRunArtifacts(ctx)
	if err != nil {
		return false, fmt.Errorf("list run artifacts for blob %s: %w", blob.Digest, err)
	}

	name := domainartifact.BlobArtifactName(in.Prefix, blob.Hex(), in.Name)

	for _, candidate := range domainartifact.BlobArtifactCandidates(in.Prefix, blob.Hex(), names) {
		if candidate != name {
			return false, nil
		}
	}

	_, upErr := store.UploadRunArtifact(ctx, provider.RunArtifactUpload{
		Name:          name,
		Files:         []string{blob.abs},
		RetentionDays: in.RetentionDays,
		IfNoFiles:     provider.IfNoFilesError,
	})
	if upErr == nil {
		return true, nil
	}

	// Another job may have stored the same digest while this upload ran; its
	// artifact is the blob the store needs, so the refusal does not matter.
	// This upload's own name counts only when it verifiably holds the blob.
	if again, listErr := store.ListRunArtifacts(ctx); listErr == nil {
		for _, candidate := range domainartifact.BlobArtifactCandidates(in.Prefix, blob.Hex(), again) {
			if candidate != name {
				return false, nil
			}

			if verifyErr := verifyStoredBlob(ctx, store, name, blob.BlobRef); verifyErr == nil {
				return false, nil
			} else if ctx.Err() == nil {
				upErr = errors.Join(upErr, verifyErr)
			}
		}
	}

	return false, fmt.Errorf("store blob %s as %q: %w", blob.Digest, name, upErr)
}

// verifyStoredBlob downloads the blob artifact name into a private temporary
// directory and checks its size and digest against ref.
func verifyStoredBlob(ctx context.Context, store provider.RunArtifactDownloader, name string, ref domainartifact.BlobRef) error {
	check, err := os.MkdirTemp("", "forge-ci-blob-check-")
	if err != nil {
		return fmt.Errorf("create blob check directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(check) }()

	return restoreFrom(ctx, store, name, check, ref, filepath.Join(check, "verified"))
}

// DownloadWithBlobStore downloads the artifact in.Name into in.Dir and
// restores every blob its manifest lists from the run's blob store. Each
// blob is checked against its digest and size before it is written into
// place; a blob the store lacks, or holds only with other bytes, is a hard
// error naming the digest. The manifest is removed, so the directory is the
// one that was uploaded.
func DownloadWithBlobStore(ctx context.Context, store blobDownloadRoles, sink domainci.OutputSink, out io.Writer, in BlobStoreDownload) (provider.RunArtifactInfo, error) {
	if err := domainartifact.ValidateBlobPrefix(in.Prefix); err != nil {
		return provider.RunArtifactInfo{}, err
	}

	if in.Pattern != "" {
		return provider.RunArtifactInfo{}, fmt.Errorf("a blob store download takes --name, not --pattern: %w", errs.ErrUsage)
	}

	info, err := Download(ctx, store, sink, out, in.RunArtifactDownload)
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}

	manifestPath := filepath.Join(in.Dir, domainartifact.BlobManifestName)

	body, err := os.ReadFile(manifestPath) //nolint:gosec // fixed name inside the caller's destination.
	if err != nil {
		return provider.RunArtifactInfo{}, fmt.Errorf("artifact %q has no blob manifest (uploaded without --blob-store?): %w", in.Name, errors.Join(err, errs.ErrValidation))
	}

	manifest, err := domainartifact.ParseBlobManifest(body)
	if err != nil {
		return provider.RunArtifactInfo{}, fmt.Errorf("artifact %q: %w", in.Name, err)
	}

	var names []string
	if len(manifest.Blobs) > 0 {
		names, err = store.ListRunArtifacts(ctx)
		if err != nil {
			return provider.RunArtifactInfo{}, fmt.Errorf("list run artifacts: %w", err)
		}
	}

	for _, ref := range manifest.Blobs {
		if err := restoreBlob(ctx, store, in, names, ref); err != nil {
			return provider.RunArtifactInfo{}, err
		}

		info.Bytes += ref.Size
		info.FileCount++
	}

	if err := os.Remove(manifestPath); err != nil {
		return provider.RunArtifactInfo{}, fmt.Errorf("remove blob manifest: %w", err)
	}

	info.FileCount--
	info.Bytes -= int64(len(body))

	if out != nil {
		_, _ = fmt.Fprintf(out, "Restored %d blobs from blob store %s → %s\n", len(manifest.Blobs), in.Prefix, in.Dir)
	}

	return info, nil
}

// restoreBlob tries each artifact that stores the digest, in name order,
// and writes the first whose bytes match it.
func restoreBlob(ctx context.Context, store provider.RunArtifactDownloader, in BlobStoreDownload, names []string, ref domainartifact.BlobRef) error {
	dest, err := domainartifact.SafeJoin(in.Dir, ref.Path)
	if err != nil {
		return err
	}

	if _, statErr := os.Lstat(dest); !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("blob %s: %q already exists in the artifact: %w", ref.Digest, ref.Path, errs.ErrValidation)
	}

	candidates := domainartifact.BlobArtifactCandidates(in.Prefix, ref.Hex(), names)
	if len(candidates) == 0 {
		return fmt.Errorf("blob %s is missing: no artifact %s-%s-* in the run: %w", ref.Digest, in.Prefix, ref.Hex(), errs.ErrValidation)
	}

	var failures []string

	for _, candidate := range candidates {
		tryErr := restoreFrom(ctx, store, candidate, in.Dir, ref, dest)
		if tryErr == nil {
			return nil
		}

		if ctx.Err() != nil {
			return tryErr
		}

		failures = append(failures, tryErr.Error())
	}

	return fmt.Errorf("blob %s: no stored copy matches its digest (%s): %w", ref.Digest, strings.Join(failures, "; "), errs.ErrValidation)
}

func restoreFrom(ctx context.Context, store provider.RunArtifactDownloader, name, dir string, ref domainartifact.BlobRef, dest string) error {
	tmp, err := os.MkdirTemp(filepath.Dir(filepath.Clean(dir)), ".forge-ci-blob-")
	if err != nil {
		return fmt.Errorf("create blob staging: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if _, err := store.DownloadRunArtifact(ctx, provider.RunArtifactDownload{Name: name, Dir: tmp}); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	src, err := os.Open(filepath.Join(tmp, ref.Hex())) //nolint:gosec // digest-named file inside a private temp directory.
	if err != nil {
		return fmt.Errorf("%s: does not hold the blob file: %w", name, err)
	}
	defer func() { _ = src.Close() }()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { //nolint:gosec // a restored tree is as readable as the forge download's.
		return fmt.Errorf("create %q: %w", filepath.Dir(dest), err)
	}

	return writeVerified(src, dest, name, ref)
}

// writeVerified copies src to dest while hashing it and keeps dest only when
// size and digest match the reference.
func writeVerified(src io.Reader, dest, name string, ref domainartifact.BlobRef) error {
	// A restored file gets the mode any file the job writes gets, as the
	// forge's own download gives it; see the Forgejo adapter.
	dst, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644) //nolint:gosec // dest passed SafeJoin.
	if err != nil {
		return fmt.Errorf("create %q: %w", dest, err)
	}

	limited := io.LimitReader(src, ref.Size+1)
	gotHex, gotSize, copyErr := domainartifact.SHA256Reader(io.TeeReader(limited, dst))

	closeErr := dst.Close()

	switch {
	case copyErr != nil:
		err = fmt.Errorf("%s: write %q: %w", name, dest, copyErr)
	case closeErr != nil:
		err = fmt.Errorf("%s: close %q: %w", name, dest, closeErr)
	case gotSize != ref.Size || gotHex != ref.Hex():
		err = fmt.Errorf("%s holds %d bytes with sha256:%s, not %d bytes of %s: %w", name, gotSize, gotHex, ref.Size, ref.Digest, errs.ErrValidation)
	}

	if err != nil {
		_ = os.Remove(dest)

		return err
	}

	return nil
}

// verifyFileDigest checks a local blob against the digest its path names, so
// the store never takes a file under a digest it does not have.
func verifyFileDigest(entry domainartifact.UploadEntry, digestHex string) error {
	file, err := domainartifact.OpenUploadEntry(entry)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	gotHex, gotSize, err := domainartifact.SHA256Reader(file)
	if err != nil {
		return fmt.Errorf("hash blob %q: %w", entry.RelPath, err)
	}

	if gotHex != digestHex || gotSize != entry.Size {
		return fmt.Errorf("blob %q holds sha256:%s, not the digest its name gives: %w", entry.RelPath, gotHex, errs.ErrValidation)
	}

	return nil
}
