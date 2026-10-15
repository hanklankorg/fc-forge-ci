// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/diggsweden/forge-ci/internal/cliio"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/retry"
)

const layoutRecordName = "forge-ci-image.json"

// LayoutInspector reads and verifies local image content without registry I/O.
type LayoutInspector interface {
	InspectLayout(ctx context.Context, directory string) (domaincontainer.LayoutImage, error)
}

// LayoutRegistry publishes the same bytes checked by LayoutInspector.
type LayoutRegistry interface {
	LayoutInspector
	ImagePublishRegistry
}

// LayoutRecordInput binds a local artifact to independently supplied build policy.
type LayoutRecordInput struct {
	Directory string
	ImageRef  string
	Platform  string
	SourceSHA string
}

// ImageLayoutRecord travels with the layout through the forge's run-scoped
// artifact store. It is an integrity/binding record, not a signature or scan proof.
type ImageLayoutRecord struct {
	ImageRef  string `json:"imageRef"`
	Platform  string `json:"platform"`
	SourceSHA string `json:"sourceSHA"`
	Digest    string `json:"digest"`
}

// RecordImageLayout runs only after the producing job's build/scan succeeds.
func RecordImageLayout(ctx context.Context, inspector LayoutInspector, in LayoutRecordInput) (*ImageLayoutRecord, error) {
	if err := validateLayoutInput(in); err != nil {
		return nil, err
	}

	image, err := inspector.InspectLayout(ctx, in.Directory)
	if err != nil {
		return nil, err
	}

	if err := matchLayoutImage(image, in, image.Digest); err != nil {
		return nil, err
	}

	record := &ImageLayoutRecord{ImageRef: in.ImageRef, Platform: in.Platform, SourceSHA: in.SourceSHA, Digest: image.Digest}
	if err := writeSignerJSON(filepath.Join(in.Directory, layoutRecordName), record); err != nil {
		return nil, err
	}

	return record, nil
}

// PublishImageLayouts validates the COMPLETE expected set before the first
// registry write. The workflow must additionally depend on all producing jobs;
// metadata cannot establish whether their configured scans succeeded.
func PublishImageLayouts(ctx context.Context, registry LayoutRegistry, out io.Writer, inputs []LayoutRecordInput, attempts int, delay time.Duration) (map[string]string, error) {
	digests, err := verifyImageLayouts(ctx, registry, inputs)
	if err != nil {
		return nil, err
	}

	for _, in := range inputs {
		digest := digests[in.Platform]

		ref := in.ImageRef
		if domaincontainer.ValidRepositoryName(ref) {
			ref += "@" + digest
		}

		pushed, err := retry.Do(ctx, out, retry.Attempts(attempts, defaultSignerImageAttempts), retry.Delay(delay, defaultSignerImageRetryDelay), func() (string, error) {
			return registry.PushLayout(ctx, in.Directory, ref)
		})
		if err != nil {
			return nil, err
		}

		if pushed != digest {
			return nil, fmt.Errorf("published layout digest changed: %w", errs.ErrValidation)
		}

		if _, err := verifyPublishedLayout(ctx, registry, out, ref, digest, attempts, delay); err != nil {
			return nil, err
		}
	}

	return digests, nil
}

func verifyImageLayouts(ctx context.Context, inspector LayoutInspector, inputs []LayoutRecordInput) (map[string]string, error) { //nolint:cyclop // linear all-input binding/content checks, before the separate upload phase.
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no staged images: %w", errs.ErrMissingInput)
	}

	digests := make(map[string]string, len(inputs))
	for _, in := range inputs {
		if err := validateLayoutInput(in); err != nil {
			return nil, err
		}

		if digests[in.Platform] != "" {
			return nil, fmt.Errorf("duplicate staged platform %s: %w", in.Platform, errs.ErrValidation)
		}

		record, err := readImageLayoutRecord(in.Directory)
		if err != nil {
			return nil, err
		}

		if record.ImageRef != in.ImageRef || record.Platform != in.Platform || record.SourceSHA != in.SourceSHA {
			return nil, fmt.Errorf("staged image repository, platform or source differs from publication inputs: %w", errs.ErrValidation)
		}

		image, err := inspector.InspectLayout(ctx, in.Directory)
		if err != nil {
			return nil, err
		}

		if err := matchLayoutImage(image, in, record.Digest); err != nil {
			return nil, err
		}

		digests[in.Platform] = record.Digest
	}

	return digests, nil
}

// LayoutSetInputs admits exactly the selected platforms from artifact download
// subdirectories. Folder names are transport details; metadata binds identity.
func LayoutSetInputs(directory, image, sourceSHA string, platforms []string) ([]LayoutRecordInput, error) { //nolint:cyclop // cardinality, uniqueness and completeness form one artifact-set admission boundary.
	if !domaincontainer.ValidRepositoryName(image) {
		return nil, fmt.Errorf("layout set publication requires a tag-free repository: %w", errs.ErrUsage)
	}

	root, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	entries, err := fsReadLayoutDirectories(root)
	if err != nil {
		return nil, err
	}

	if len(entries) != len(platforms) || len(platforms) == 0 {
		return nil, fmt.Errorf("staged artifact count differs from selected platforms: %w", errs.ErrValidation)
	}

	byPlatform := make(map[string]string, len(entries))
	for _, entry := range entries {
		path := filepath.Join(directory, entry)

		record, err := readImageLayoutRecord(path)
		if err != nil {
			return nil, err
		}

		if byPlatform[record.Platform] != "" {
			return nil, fmt.Errorf("duplicate staged platform: %w", errs.ErrValidation)
		}

		byPlatform[record.Platform] = path
	}

	inputs := make([]LayoutRecordInput, 0, len(platforms))
	for _, platform := range platforms {
		path := byPlatform[platform]
		if path == "" {
			return nil, fmt.Errorf("missing staged platform %s: %w", platform, errs.ErrMissingInput)
		}

		inputs = append(inputs, LayoutRecordInput{Directory: path, ImageRef: image, SourceSHA: sourceSHA, Platform: platform})
		delete(byPlatform, platform)
	}

	return inputs, nil
}

func fsReadLayoutDirectories(root *os.Root) ([]string, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}

	var names []string

	for _, entry := range entries {
		if !entry.IsDir() {
			return nil, fmt.Errorf("layout set must contain only artifact directories: %w", errs.ErrValidation)
		}

		names = append(names, entry.Name())
	}

	return names, nil
}

func readImageLayoutRecord(directory string) (*ImageLayoutRecord, error) {
	root, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	body, err := cliio.ReadFileInRoot(root, layoutRecordName)
	if err != nil {
		return nil, err
	}

	var record ImageLayoutRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return nil, fmt.Errorf("parse layout record: %w: %w", err, errs.ErrMalformedInput)
	}

	return &record, nil
}

func validateLayoutInput(in LayoutRecordInput) error {
	if (!domaincontainer.ValidRepositoryName(in.ImageRef) && !domaincontainer.ValidTaggedRef(in.ImageRef)) || !git.ValidCommitSHA(in.SourceSHA) {
		return fmt.Errorf("staged image requires an explicit repository/tag and full source commit: %w", errs.ErrUsage)
	}

	if in.Platform != "linux/amd64" && in.Platform != "linux/arm64" {
		return fmt.Errorf("staged platform must be linux/amd64 or linux/arm64: %w", errs.ErrUsage)
	}

	return nil
}

func matchLayoutImage(image domaincontainer.LayoutImage, in LayoutRecordInput, digest string) error {
	if !domaincontainer.ValidDigest(digest) || image.Digest != digest || image.Platform != in.Platform || image.Revision != in.SourceSHA {
		return fmt.Errorf("layout content differs from recorded digest, platform or OCI revision: %w", errs.ErrValidation)
	}

	return nil
}
