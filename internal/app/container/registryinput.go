// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const registryInputManifest = "forge-ci-source-manifest.json"

// RegistryInputLoader imports verified local bytes into native builder storage.
// It receives no registry reference or authentication file.
type RegistryInputLoader interface {
	LoadRegistryInput(ctx context.Context, directory, local string, out io.Writer) error
}

// ImportRegistryInput verifies the downloaded artifact before the builder sees
// any bytes. The local name is derived from the verified child digest; it cannot
// replace a remote registry name or accidentally load an unverified variant.
func ImportRegistryInput(ctx context.Context, inspector LayoutInspector, loader RegistryInputLoader, out io.Writer, ref, platform, directory string) (string, error) {
	image, err := VerifyRegistryInput(ctx, inspector, ref, platform, directory)
	if err != nil {
		return "", err
	}

	local := "localhost/forge-ci-input:" + strings.TrimPrefix(image.Digest, "sha256:")
	if err = loader.LoadRegistryInput(ctx, directory, local, out); err != nil {
		return "", err
	}

	return local, nil
}

// RegistryInputReader fetches pinned inputs only. It cannot build or publish.
type RegistryInputReader interface {
	LayoutInspector
	Manifest(ctx context.Context, ref string) ([]byte, error)
	CopyDockerDigestToOCILayout(ctx context.Context, repository, digest, directory, osName, arch string) error
}

// FetchRegistryInput retains a pinned parent image for a later secret-free job.
// The original index/manifest is retained as proof that the selected child is
// part of the independently supplied input digest, not just a claimed binding.
func FetchRegistryInput(ctx context.Context, registry RegistryInputReader, ref, platform, directory string) error {
	repository, digest, err := registryInputIdentity(ref, platform)
	if err != nil {
		return err
	}

	if _, err = os.Lstat(directory); !os.IsNotExist(err) {
		return fmt.Errorf("registry input destination must not exist: %w", errs.ErrUsage)
	}

	manifest, err := registry.Manifest(ctx, ref)
	if err != nil {
		return err
	}

	if err = registryInputManifestDigest(manifest, digest); err != nil {
		return err
	}

	if err = registry.CopyDockerDigestToOCILayout(ctx, repository, digest, directory, "linux", strings.TrimPrefix(platform, "linux/")); err != nil {
		return err
	}

	if err = os.WriteFile(filepath.Join(directory, registryInputManifest), manifest, 0o600); err != nil {
		return err
	}

	_, err = VerifyRegistryInput(ctx, registry, ref, platform, directory)

	return err
}

// VerifyRegistryInput validates local content and its source membership without
// registry credentials or network access. Neither index nor receipt is trusted
// independently of the input digest supplied by the admitted build policy.
func VerifyRegistryInput(ctx context.Context, inspector LayoutInspector, ref, platform, directory string) (*domaincontainer.LayoutImage, error) {
	_, digest, err := registryInputIdentity(ref, platform)
	if err != nil {
		return nil, err
	}

	manifest, err := cliio.ReadRegularFile(filepath.Join(directory, registryInputManifest))
	if err != nil {
		return nil, err
	}

	if err = registryInputManifestDigest(manifest, digest); err != nil {
		return nil, err
	}

	image, err := inspector.InspectLayout(ctx, directory)
	if err != nil {
		return nil, err
	}

	if image.Platform != platform {
		return nil, fmt.Errorf("registry input platform differs from build policy: %w", errs.ErrValidation)
	}

	if image.Digest == digest {
		return &image, nil
	}

	var index struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err = json.Unmarshal(manifest, &index); err != nil {
		return nil, fmt.Errorf("invalid registry input index: %w", errs.ErrMalformedInput)
	}

	for _, child := range index.Manifests {
		if child.Digest == image.Digest && child.Platform.OS+"/"+child.Platform.Architecture == platform {
			return &image, nil
		}
	}

	return nil, fmt.Errorf("registry input layout is not a selected child of the pinned source: %w", errs.ErrValidation)
}

func registryInputIdentity(ref, platform string) (string, string, error) {
	repository, digest, ok := strings.Cut(ref, "@")
	if !ok || !domaincontainer.ValidRepositoryName(repository) || !domaincontainer.ValidDigest(digest) {
		return "", "", fmt.Errorf("registry input requires a digest-pinned repository: %w", errs.ErrUsage)
	}

	if platform != "linux/"+domaincontainer.ArchAMD64 && platform != "linux/"+domaincontainer.ArchARM64 {
		return "", "", fmt.Errorf("registry input requires a supported native Linux platform: %w", errs.ErrUsage)
	}

	return repository, digest, nil
}

func registryInputManifestDigest(manifest []byte, expected string) error {
	hash := sha256.Sum256(manifest)
	if "sha256:"+hex.EncodeToString(hash[:]) != expected {
		return fmt.Errorf("registry input source manifest differs from pinned digest: %w", errs.ErrValidation)
	}

	return nil
}
