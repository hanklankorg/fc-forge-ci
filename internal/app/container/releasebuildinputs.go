// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// ReleaseBuildInputRegistry reads authenticated caches and pinned parent images.
// Neither interface can execute a consumer build or publish an image.
type ReleaseBuildInputRegistry interface {
	ArchitectureCache
	RegistryInputReader
}

type releaseBuildInputsRecord struct {
	Parents   map[string]string `json:"parents"`
	SourceSHA string            `json:"sourceSHA"`
	Arch      string            `json:"arch"`
	CacheHit  bool              `json:"cacheHit"`
}

// FetchReleaseBuildInputs is the credential-holding half of image builds. It
// writes only run-scoped layouts, with no checkout or executable consumer code.
func FetchReleaseBuildInputs(ctx context.Context, registry ReleaseBuildInputRegistry, out io.Writer, input SignerImageBuildArchInput, parents map[string]string, directory string) error {
	if err := pipeline.ValidateRegistryInputs(pipeline.ReleaseImageBuild{BuildArgs: strings.Join(input.BuildArgs, "\n"), RegistryInputs: parents}); err != nil {
		return err
	}

	if _, err := deriveSignerImageArch(input, false); err != nil {
		return err
	}

	// The directory is new, as the build's metadata directory is renewed,
	// and its missing parents are created as real directories: the build
	// reads <inputs-dir>/<name>-arch-<arch>, so a run fetches into that.
	if err := newBuildInputsDir(directory); err != nil {
		return err
	}

	cacheHit := false

	if input.CacheTag != "" {
		cache := input
		cache.MetadataDir = filepath.Join(directory, "cache")

		metadata, err := StageCachedSignerImageArch(ctx, registry, out, cache)
		if err != nil && !errors.Is(err, errs.ErrMissingInput) {
			return err
		}

		cacheHit = metadata != nil
	}

	for _, key := range registryInputKeys(parents) {
		if err := FetchRegistryInput(ctx, registry, parents[key], "linux/"+input.Arch, filepath.Join(directory, key)); err != nil {
			return fmt.Errorf("fetch image build input %s: %w", key, err)
		}
	}

	body, err := json.Marshal(releaseBuildInputsRecord{parents, input.SourceSHA, input.Arch, cacheHit})
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(directory, "inputs.json"), body, 0o600)
}

// newBuildInputsDir creates directory, which must not exist yet, under its
// parents, creating those that are missing and refusing a link among them.
func newBuildInputsDir(directory string) error {
	root, err := pathsafe.MkdirRoot(filepath.Dir(directory), 0o700)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	return root.Mkdir(filepath.Base(directory), 0o700)
}

// PreparedBuildRegistry reads local layouts only. Implementations used by the
// build command must not hold registry credentials.
type PreparedBuildRegistry interface {
	LayoutInspector
	CopyOCILayoutToOCILayout(ctx context.Context, source, destination, osName, arch string) error
}

// StageReleaseBuildWithInputs consumes the fetch job's layouts without registry
// access. Every parent is verified before the first builder operation. Cached
// output still passes the ordinary complete-set scan/publication barrier.
func StageReleaseBuildWithInputs(ctx context.Context, tool SignerImageTool, loader RegistryInputLoader, registry PreparedBuildRegistry, out io.Writer, input SignerImageBuildArchInput, parents map[string]string, directory string) (*SignerImageArchMetadata, error) {
	if err := CheckSignerImageBuilderIdentity(tool, input.BuilderMaterialID); err != nil {
		return nil, err
	}

	if err := pipeline.ValidateRegistryInputs(pipeline.ReleaseImageBuild{BuildArgs: strings.Join(input.BuildArgs, "\n"), RegistryInputs: parents}); err != nil {
		return nil, err
	}

	if input.AuthFile != "" {
		return nil, fmt.Errorf("prepared image builds must not receive registry credentials: %w", errs.ErrUsage)
	}

	derived, err := deriveSignerImageArch(input, false)
	if err != nil {
		return nil, err
	}

	body, err := cliio.ReadRegularFile(filepath.Join(directory, "inputs.json"))
	if err != nil {
		return nil, err
	}

	var prepared releaseBuildInputsRecord

	if err = json.Unmarshal(body, &prepared); err != nil {
		return nil, err
	}

	if prepared.SourceSHA != input.SourceSHA || prepared.Arch != input.Arch || !reflect.DeepEqual(prepared.Parents, parents) {
		return nil, fmt.Errorf("prepared registry inputs differ from admitted build policy: %w", errs.ErrValidation)
	}

	for _, key := range registryInputKeys(parents) {
		if _, err = VerifyRegistryInput(ctx, registry, parents[key], derived.Platform, filepath.Join(directory, key)); err != nil {
			return nil, fmt.Errorf("verify image build input %s: %w", key, err)
		}
	}

	if prepared.CacheHit {
		return stagePreparedArchitectureCache(ctx, registry, input, derived, filepath.Join(directory, "cache", "layout"))
	}

	arguments, err := importReleaseBuildParents(ctx, loader, registry, out, input.BuildArgs, parents, derived.Platform, directory)
	if err != nil {
		return nil, err
	}

	input.BuildArgs = arguments
	input.CacheTag = "" // Cache lookup occurred in the isolated fetch job, never here.
	input.CacheLabels = nil

	return StageSignerImageArch(ctx, tool, registry, out, input)
}

func stagePreparedArchitectureCache(ctx context.Context, registry PreparedBuildRegistry, input SignerImageBuildArchInput, derived signerImageArchDerived, cacheLayout string) (*SignerImageArchMetadata, error) {
	if input.CacheTag == "" {
		return nil, fmt.Errorf("unexpected prepared architecture cache: %w", errs.ErrValidation)
	}

	if err := recreateSignerMetadataDir(derived.MetadataDir); err != nil {
		return nil, err
	}

	image, err := registry.InspectLayout(ctx, cacheLayout)
	if err != nil {
		return nil, err
	}

	if image.Platform != derived.Platform || image.Revision != input.SourceSHA {
		return nil, fmt.Errorf("prepared cache differs from planned source or platform: %w", errs.ErrValidation)
	}

	if err = matchMultiarchLabels(image.Labels, input.CacheLabels); err != nil {
		return nil, err
	}

	layout := filepath.Join(derived.MetadataDir, "layout")
	if err = registry.CopyOCILayoutToOCILayout(ctx, cacheLayout, layout, "linux", input.Arch); err != nil {
		return nil, err
	}

	record, err := RecordImageLayout(ctx, registry, LayoutRecordInput{Directory: layout, ImageRef: derived.ImageTag, Platform: derived.Platform, SourceSHA: input.SourceSHA})
	if err != nil {
		return nil, err
	}

	if record.Digest != image.Digest {
		return nil, fmt.Errorf("prepared cache digest changed during import: %w", errs.ErrValidation)
	}

	metadata := &SignerImageArchMetadata{Arch: input.Arch, Tag: derived.ImageTag, Digest: image.Digest, Ref: derived.ImageRepository + "@" + image.Digest}
	if err = writeSignerJSON(filepath.Join(derived.MetadataDir, derived.Name+"-"+input.Arch+".json"), metadata); err != nil {
		return nil, err
	}

	return metadata, nil
}

func importReleaseBuildParents(ctx context.Context, loader RegistryInputLoader, registry PreparedBuildRegistry, out io.Writer, buildArgs []string, parents map[string]string, platform, directory string) ([]string, error) {
	arguments := append([]string(nil), buildArgs...)

	for _, key := range registryInputKeys(parents) {
		if loader == nil {
			return nil, fmt.Errorf("native builder cannot import prepared registry inputs: %w", errs.ErrUsage)
		}

		local, importErr := ImportRegistryInput(ctx, registry, loader, out, parents[key], platform, filepath.Join(directory, key))
		if importErr != nil {
			return nil, importErr
		}

		found := false

		for index, argument := range arguments {
			if argument == key+"="+parents[key] {
				arguments[index] = key + "=" + local
				found = true
			}
		}

		if !found {
			return nil, fmt.Errorf("prepared parent has no matching build argument: %w", errs.ErrInvalidConfig)
		}
	}

	return arguments, nil
}

func registryInputKeys(parents map[string]string) []string {
	keys := make([]string, 0, len(parents))
	for key := range parents {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
