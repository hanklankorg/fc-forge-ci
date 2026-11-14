// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// CacheReusePrefix precedes the reused architecture image reference in the
// signer output; the live image-plan oracle counts it as reuse evidence.
const CacheReusePrefix = "Architecture cache reused by digest: "

// ArchitectureCache is read-only. A cache slot is not release authority: its
// immutable bytes still pass the normal complete-set scan/publication barrier.
type ArchitectureCache interface {
	LayoutInspector
	ResolveDigest(ctx context.Context, ref string) (string, error)
	ImagePlatform(ctx context.Context, ref string) (string, string, error)
	Labels(ctx context.Context, ref string) (map[string]string, error)
	CopyDockerDigestToOCILayout(ctx context.Context, ref, digest, directory, osName, arch string) error
}

var errArchitectureCacheMiss = errors.New("architecture cache miss")

// StageCachedSignerImageArch fetches a verified architecture cache layout without
// ever executing a builder or the consumer's source. Authenticated callers can
// run this in a credential-isolated job and hand the layout to later jobs.
// A cache miss is explicit; it never falls back to building beside credentials.
func StageCachedSignerImageArch(ctx context.Context, registry ArchitectureCache, out io.Writer, in SignerImageBuildArchInput) (*SignerImageArchMetadata, error) {
	if registry == nil || in.CacheTag == "" {
		return nil, fmt.Errorf("cache-only staging requires a registry and cache tag: %w", errs.ErrUsage)
	}

	derived, err := deriveSignerImageArch(in, false)
	if err != nil {
		return nil, err
	}

	if err = recreateSignerMetadataDir(derived.MetadataDir); err != nil {
		return nil, err
	}

	metadata, err := stageCachedArchitecture(ctx, registry, out, in, derived)
	if errors.Is(err, errArchitectureCacheMiss) {
		return nil, fmt.Errorf("no matching architecture cache: %w", errs.ErrMissingInput)
	}

	return metadata, err
}

func validateArchitectureCachePublication(in SignerImageAssembleInput, derived signerImageManifestDerived) error {
	if in.CacheTagPrefix == "" {
		return nil
	}

	if in.Evidence == nil || in.Evidence.Gate == nil {
		return fmt.Errorf("architecture cache publication requires the complete-set scan gate: %w", errs.ErrUsage)
	}

	for _, arch := range derived.Archs {
		if !domaincontainer.ValidTaggedRef(derived.ImageRepository + ":" + in.CacheTagPrefix + "-" + arch) {
			return fmt.Errorf("invalid architecture cache tag prefix: %w", errs.ErrUsage)
		}
	}

	return nil
}

func publishArchitectureCaches(ctx context.Context, registry LayoutRegistry, out io.Writer, in SignerImageAssembleInput, derived signerImageManifestDerived, inputs []LayoutRecordInput) error {
	if in.CacheTagPrefix == "" {
		return nil
	}

	for idx, input := range inputs {
		ref := derived.ImageRepository + ":" + in.CacheTagPrefix + "-" + derived.Archs[idx]

		image, err := registry.InspectLayout(ctx, input.Directory)
		if err != nil {
			return err
		}

		digest, err := retry.Do(ctx, out, retry.Attempts(in.RetryAttempts, defaultSignerImageAttempts), retry.Delay(in.RetryDelay, defaultSignerImageRetryDelay), func() (string, error) {
			return registry.PushLayout(ctx, input.Directory, ref)
		})
		if err != nil {
			return err
		}

		if digest != image.Digest {
			return fmt.Errorf("architecture cache upload changed digest: %w", errs.ErrValidation)
		}

		if _, err := verifyPublishedLayout(ctx, registry, out, ref, digest, in.RetryAttempts, in.RetryDelay); err != nil {
			return err
		}
	}

	return nil
}

func stageCachedArchitecture(ctx context.Context, inspector LayoutInspector, out io.Writer, in SignerImageBuildArchInput, derived signerImageArchDerived) (*SignerImageArchMetadata, error) {
	if in.CacheTag == "" {
		return nil, errArchitectureCacheMiss
	}

	registry, ok := inspector.(ArchitectureCache)
	if !ok || len(in.CacheLabels) == 0 || !domaincontainer.ValidTaggedRef(derived.ImageRepository+":"+in.CacheTag) {
		return nil, fmt.Errorf("architecture cache requires staging, required labels and a local repository tag: %w", errs.ErrUsage)
	}

	if _, err := parseRequiredImageLabels(in.CacheLabels); err != nil {
		return nil, err
	}

	digest, err := reusableArchitectureDigest(ctx, registry, in, derived.ImageRepository)
	if err != nil {
		return nil, err
	}

	directory := filepath.Join(derived.MetadataDir, "layout")
	if copyErr := registry.CopyDockerDigestToOCILayout(ctx, derived.ImageRepository, digest, directory, "linux", in.Arch); copyErr != nil {
		return nil, copyErr
	}

	record, err := RecordImageLayout(ctx, registry, LayoutRecordInput{Directory: directory, ImageRef: derived.ImageTag, Platform: derived.Platform, SourceSHA: in.SourceSHA})
	if err != nil {
		return nil, err
	}

	if record.Digest != digest {
		return nil, fmt.Errorf("cache copy differs from resolved digest: %w", errs.ErrValidation)
	}

	if labelErr := matchStagedMultiarchLabels(ctx, registry, directory, digest, in.CacheLabels); labelErr != nil {
		return nil, labelErr
	}

	meta := &SignerImageArchMetadata{Arch: in.Arch, Tag: derived.ImageTag, Digest: digest, Ref: derived.ImageRepository + "@" + digest}
	if writeErr := writeSignerJSON(filepath.Join(derived.MetadataDir, derived.Name+"-"+in.Arch+".json"), meta); writeErr != nil {
		return nil, writeErr
	}

	_, _ = fmt.Fprintf(out, "%s%s\n", CacheReusePrefix, meta.Ref)

	return meta, nil
}

func reusableArchitectureDigest(ctx context.Context, registry ArchitectureCache, in SignerImageBuildArchInput, repository string) (string, error) {
	digest, err := registry.ResolveDigest(ctx, repository+":"+in.CacheTag)
	if errors.Is(err, errs.ErrMissingInput) {
		return "", errArchitectureCacheMiss
	}

	if err != nil {
		return "", err
	}

	if !domaincontainer.ValidDigest(digest) {
		return "", fmt.Errorf("cache slot returned an invalid digest: %w", errs.ErrValidation)
	}

	immutable := repository + "@" + digest

	osName, arch, err := registry.ImagePlatform(ctx, immutable)
	if err != nil {
		return "", err
	}

	labels, err := registry.Labels(ctx, immutable)
	if err != nil {
		return "", err
	}

	if osName != "linux" || arch != in.Arch || labels["org.opencontainers.image.revision"] != in.SourceSHA || matchMultiarchLabels(labels, in.CacheLabels) != nil {
		return "", errArchitectureCacheMiss
	}

	return digest, nil
}
