// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/diggsweden/forge-ci/internal/cliio"
	domainartifact "github.com/diggsweden/forge-ci/internal/domain/artifact"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// InspectLayout verifies the single manifest, config and every compressed layer
// against their recorded SHA-256 and size, entirely offline. No tar extraction
// or external layer URL is needed to prove that these are the staged bytes.
func (*Adapter) InspectLayout(ctx context.Context, directory string) (domaincontainer.LayoutImage, error) {
	root, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return domaincontainer.LayoutImage{}, err
	}
	defer func() { _ = root.Close() }()

	if err = checkLayoutFiles(ctx, root); err != nil {
		return domaincontainer.LayoutImage{}, err
	}

	descriptor, err := layoutImageDescriptor(root)
	if err != nil {
		return domaincontainer.LayoutImage{}, err
	}

	config, err := verifiedLayoutConfig(ctx, root, descriptor)
	if err != nil {
		return domaincontainer.LayoutImage{}, err
	}

	platform := config.OS + "/" + config.Architecture
	if config.Variant != "" && (config.Architecture != domaincontainer.ArchARM64 || config.Variant != "v8") {
		platform += "/" + config.Variant
	}

	return domaincontainer.LayoutImage{Digest: descriptor.Digest.String(), Platform: platform, Revision: config.Config.Labels["org.opencontainers.image.revision"]}, nil
}

func layoutImageDescriptor(root *os.Root) (v1.Descriptor, error) {
	header, err := cliio.ReadFileInRoot(root, "oci-layout")
	if err != nil {
		return v1.Descriptor{}, err
	}

	var layoutVersion struct {
		Version string `json:"imageLayoutVersion"`
	}
	if err = json.Unmarshal(header, &layoutVersion); err != nil || layoutVersion.Version != "1.0.0" {
		return v1.Descriptor{}, fmt.Errorf("unsupported OCI layout version: %w", errs.ErrValidation)
	}

	indexBody, err := cliio.ReadFileInRoot(root, "index.json")
	if err != nil {
		return v1.Descriptor{}, err
	}

	var index v1.IndexManifest
	if err = json.Unmarshal(indexBody, &index); err != nil || index.SchemaVersion != 2 || len(index.Manifests) != 1 || !index.Manifests[0].MediaType.IsImage() {
		return v1.Descriptor{}, fmt.Errorf("layout must contain exactly one image manifest: %w", errs.ErrValidation)
	}

	return index.Manifests[0], nil
}

func verifiedLayoutConfig(ctx context.Context, root *os.Root, descriptor v1.Descriptor) (*v1.ConfigFile, error) {
	manifestBody, err := layoutDocument(ctx, root, descriptor)
	if err != nil {
		return nil, err
	}

	var manifest v1.Manifest
	if err = json.Unmarshal(manifestBody, &manifest); err != nil || manifest.SchemaVersion != 2 || !manifest.Config.MediaType.IsConfig() {
		return nil, fmt.Errorf("invalid layout image manifest: %w", errs.ErrMalformedInput)
	}

	configBody, err := layoutDocument(ctx, root, manifest.Config)
	if err != nil {
		return nil, err
	}

	var config v1.ConfigFile
	if err = json.Unmarshal(configBody, &config); err != nil {
		return nil, fmt.Errorf("invalid layout config: %w: %w", err, errs.ErrMalformedInput)
	}

	for _, layer := range manifest.Layers {
		if err = verifyLayoutBlob(ctx, root, layer); err != nil {
			return nil, err
		}
	}

	return &config, nil
}

func checkLayoutFiles(ctx context.Context, root *os.Root) error {
	var (
		total int64
		count int
	)

	return fs.WalkDir(root.FS(), ".", func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if err = ctx.Err(); err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		if !info.Mode().IsRegular() || info.Size() > domainartifact.MaxFileBytes {
			return fmt.Errorf("layout requires regular files within the artifact size bound: %w", errs.ErrValidation)
		}

		if err := domainartifact.ValidateAggregate(total, count, info.Size(), 1); err != nil {
			return err
		}

		total += info.Size()
		count++

		return nil
	})
}

func layoutDocument(ctx context.Context, root *os.Root, descriptor v1.Descriptor) ([]byte, error) {
	if err := verifyLayoutBlob(ctx, root, descriptor); err != nil {
		return nil, err
	}

	return cliio.ReadFileInRoot(root, "blobs/sha256/"+descriptor.Digest.Hex)
}

func verifyLayoutBlob(ctx context.Context, root *os.Root, descriptor v1.Descriptor) error {
	if !domaincontainer.ValidDigest(descriptor.Digest.String()) || descriptor.Size < 0 || descriptor.Size > domainartifact.MaxFileBytes || len(descriptor.URLs) != 0 {
		return fmt.Errorf("layout requires bounded local SHA-256 blobs: %w", errs.ErrValidation)
	}

	file, err := root.Open("blobs/sha256/" + descriptor.Digest.Hex)
	if err != nil {
		return fmt.Errorf("open layout blob: %w: %w", err, errs.ErrMissingInput)
	}

	defer func() { _ = file.Close() }()

	digest, size, err := hashLayoutBlob(ctx, io.LimitReader(file, descriptor.Size+1))
	if err != nil {
		return err
	}

	if size != descriptor.Size || digest != descriptor.Digest.Hex {
		return fmt.Errorf("layout blob %s differs from its digest or size: %w", descriptor.Digest, errs.ErrValidation)
	}

	return nil
}

func hashLayoutBlob(ctx context.Context, reader io.Reader) (string, int64, error) {
	hash := sha256.New()
	buffer := make([]byte, 128<<10)

	var size int64

	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}

		count, readErr := reader.Read(buffer)
		_, _ = hash.Write(buffer[:count])
		size += int64(count)

		if readErr == io.EOF {
			break
		}

		if readErr != nil {
			return "", 0, readErr
		}
	}

	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
