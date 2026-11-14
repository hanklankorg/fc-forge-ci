// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"context"
	"errors"
	"fmt"
	"slices"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// refNameAnnotation names an image inside an OCI layout's index.
const refNameAnnotation = "org.opencontainers.image.ref.name"

// scanRefName is the tag image evidence reads and writes in a scan layout.
const scanRefName = "scan"

// CopyOCILayoutToOCILayout copies the os/arch image tagged "scan" in
// sourceLayout into a new single-platform layout at destLayout, tagged "scan".
// It never touches a registry, so it takes no credential.
func (a *Adapter) CopyOCILayoutToOCILayout(_ context.Context, sourceLayout, destLayout, osName, arch string) error {
	if err := errors.Join(
		requireCopyInput(sourceLayout, "source layout"),
		requireCopyInput(destLayout, "destination layout"),
		requireCopyInput(osName, "platform OS"),
		requireCopyInput(arch, "platform architecture"),
	); err != nil {
		return err
	}

	idx, err := layout.ImageIndexFromPath(sourceLayout)
	if err != nil {
		return fmt.Errorf("read oci layout %s: %w: %w", sourceLayout, err, errs.ErrMalformedInput)
	}

	manifest, err := idx.IndexManifest()
	if err != nil {
		return fmt.Errorf("read oci index in %s: %w: %w", sourceLayout, err, errs.ErrMalformedInput)
	}

	taggedIndex := slices.IndexFunc(manifest.Manifests, func(desc v1.Descriptor) bool {
		return desc.Annotations[refNameAnnotation] == scanRefName
	})
	if taggedIndex < 0 {
		return fmt.Errorf("oci layout %s has no image tagged %q: %w", sourceLayout, scanRefName, errs.ErrMissingInput)
	}

	tagged := manifest.Manifests[taggedIndex]

	digest := tagged.Digest
	if tagged.MediaType.IsIndex() {
		child, childErr := idx.ImageIndex(tagged.Digest)
		if childErr != nil {
			return fmt.Errorf("read image index in %s: %w: %w", sourceLayout, childErr, errs.ErrMalformedInput)
		}

		if digest, err = platformDigest(child, sourceLayout, osName, arch); err != nil {
			return err
		}

		idx = child
	}

	img, err := idx.Image(digest)
	if err != nil {
		return fmt.Errorf("read image %s in %s: %w: %w", digest, sourceLayout, err, errs.ErrMalformedInput)
	}

	return writeScanLayout(destLayout, img)
}

// CopyDockerDigestToOCILayout copies the os/arch image of ref@digest into a new
// single-platform layout at destLayout, tagged "scan". The source is pinned by
// digest, so a tag moving between the copy and the scan cannot change it.
func (a *Adapter) CopyDockerDigestToOCILayout(ctx context.Context, ref, digest, destLayout, osName, arch string) error {
	if err := errors.Join(
		requireCopyInput(ref, "source ref"),
		requireCopyInput(digest, "source digest"),
		requireCopyInput(destLayout, "destination layout"),
		requireCopyInput(osName, "platform OS"),
		requireCopyInput(arch, "platform architecture"),
	); err != nil {
		return err
	}

	source, err := a.parse(ref + "@" + digest)
	if err != nil {
		return fmt.Errorf("parse image ref %s@%s: %w: %w", ref, digest, err, errs.ErrUsage)
	}

	desc, err := remote.Get(source, a.remoteOpts(ctx)...)
	if err != nil {
		return fmt.Errorf("fetch image %s: %w: %w", source, err, classifyRegistryError(err))
	}

	if !desc.MediaType.IsIndex() {
		img, imgErr := desc.Image()
		if imgErr != nil {
			return fmt.Errorf("read image %s: %w: %w", source, imgErr, classifyRegistryError(imgErr))
		}

		return writeScanLayout(destLayout, img)
	}

	idx, err := desc.ImageIndex()
	if err != nil {
		return fmt.Errorf("read image index %s: %w: %w", source, err, classifyRegistryError(err))
	}

	child, err := platformDigest(idx, source.String(), osName, arch)
	if err != nil {
		return err
	}

	img, err := idx.Image(child)
	if err != nil {
		return fmt.Errorf("read %s/%s image of %s: %w: %w", osName, arch, source, err, classifyRegistryError(err))
	}

	return writeScanLayout(destLayout, img)
}

// platformDigest returns the digest of the first os/arch child of idx.
func platformDigest(idx v1.ImageIndex, source, osName, arch string) (v1.Hash, error) {
	manifest, err := idx.IndexManifest()
	if err != nil {
		return v1.Hash{}, fmt.Errorf("read image index %s: %w: %w", source, err, errs.ErrMalformedInput)
	}

	platformIndex := slices.IndexFunc(manifest.Manifests, func(child v1.Descriptor) bool {
		return child.Platform != nil && child.Platform.OS == osName && child.Platform.Architecture == arch
	})
	if platformIndex < 0 {
		return v1.Hash{}, fmt.Errorf("image index %s has no %s/%s image: %w", source, osName, arch, errs.ErrMissingInput)
	}

	return manifest.Manifests[platformIndex].Digest, nil
}

// writeScanLayout writes img as the only image of a new OCI layout at dir,
// tagged "scan" so the scanners find it.
func writeScanLayout(dir string, img v1.Image) error {
	path, err := layout.Write(dir, empty.Index)
	if err != nil {
		return fmt.Errorf("create oci layout %s: %w", dir, err)
	}

	if err := path.AppendImage(img, layout.WithAnnotations(map[string]string{refNameAnnotation: scanRefName})); err != nil {
		return fmt.Errorf("write image to oci layout %s: %w: %w", dir, err, classifyRegistryError(err))
	}

	return nil
}

// requireCopyInput refuses an empty copy argument before anything is read: an
// empty platform in particular would otherwise copy an arbitrary image.
func requireCopyInput(value, what string) error {
	if value == "" {
		return fmt.Errorf("copy to oci layout: %s is empty: %w", what, errs.ErrUsage)
	}

	return nil
}
