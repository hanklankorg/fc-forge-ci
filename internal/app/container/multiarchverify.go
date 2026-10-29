// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func multiarchImageTag(repository, sourceSHA, prefix, tag string) (string, error) {
	if tag != "" && prefix != "" {
		return "", fmt.Errorf("tag and tag-prefix are mutually exclusive: %w", errs.ErrUsage)
	}

	if tag == "" {
		tag = prefix + sourceSHA
	}

	ref := repository + ":" + tag
	// Check architecture tags too, before any build or publication.
	if err := domaincontainer.ValidateManifestTags(repository, []string{ref, ref + "-amd64", ref + "-arm64"}); err != nil {
		return "", err
	}

	return ref, nil
}

func prepareSignerImageManifest(ctx context.Context, registry SignerManifestRegistry, in SignerImageAssembleInput) (signerImageManifestDerived, error) {
	derived, err := deriveSignerImageManifest(in)
	if err != nil {
		return derived, err
	}

	if registry == nil {
		return derived, fmt.Errorf("image registry is required: %w", errs.ErrUsage)
	}

	seen := make(map[string]bool, len(derived.Archs))
	// Read the complete metadata set before contacting the registry.
	for _, arch := range derived.Archs {
		ref, err := signerArchRef(in.LayoutsDir, arch, derived.ImageRepository, derived.Name, derived.ManifestTag+"-"+arch)
		if err != nil {
			return derived, err
		}

		if seen[ref] {
			return derived, fmt.Errorf("image architectures must identify distinct image digests: %w", errs.ErrValidation)
		}

		seen[ref] = true
		derived.Digests = append(derived.Digests, strings.TrimPrefix(ref, derived.ImageRepository+"@sha256:"))
	}

	for index, arch := range derived.Archs {
		ref := derived.ImageRepository + "@sha256:" + derived.Digests[index]
		if matchErr := matchRegistryImage(ctx, registry, ref, arch, in.RequiredLabels); matchErr != nil {
			return derived, matchErr
		}
	}

	return derived, nil
}

func matchRegistryImage(ctx context.Context, registry SignerManifestRegistry, ref, arch string, required []string) error {
	osName, actualArch, err := registry.ImagePlatform(ctx, ref)
	if err != nil {
		return err
	}

	if osName != "linux" || actualArch != arch {
		return fmt.Errorf("image must have platform linux/%s, got %s/%s: %w", arch, osName, actualArch, errs.ErrValidation)
	}

	if len(required) == 0 {
		return nil
	}

	labels, err := registry.Labels(ctx, ref)
	if err != nil {
		return err
	}

	return matchMultiarchLabels(labels, required)
}

func matchStagedMultiarchLabels(ctx context.Context, inspector LayoutInspector, directory, digest string, required []string) error {
	if len(required) == 0 {
		return nil
	}

	image, err := inspector.InspectLayout(ctx, directory)
	if err != nil {
		return err
	}

	if image.Digest != digest {
		return fmt.Errorf("staged label source differs from recorded digest: %w", errs.ErrValidation)
	}

	return matchMultiarchLabels(image.Labels, required)
}

func matchMultiarchLabels(actual map[string]string, required []string) error {
	labels, err := parseRequiredImageLabels(required)
	if err != nil {
		return err
	}

	for _, want := range labels {
		if value, present := actual[want.key]; !present || value != want.value {
			return fmt.Errorf("image label %s does not match required value: %w", want.key, errs.ErrValidation)
		}
	}

	return nil
}

// VerifyMultiarchImageManifest checks an immutable existing index against the
// same architecture records used for publication. It performs no registry writes;
// ManifestOutput optionally retains the checked bytes. Config checks work on any host.
func VerifyMultiarchImageManifest(ctx context.Context, registry SignerManifestRegistry, sink ci.OutputSink, ref string, in SignerImageAssembleInput) error {
	if !domaincontainer.ValidDigestPinnedRef(ref) {
		return fmt.Errorf("verify requires a tag-free digest-pinned reference: %w", errs.ErrUsage)
	}

	derived, err := prepareSignerImageManifest(ctx, registry, in)
	if err != nil {
		return err
	}

	raw, err := verifyIndexContent(ctx, registry, ref, derived)
	if err != nil {
		return err
	}

	if err = writeVerifiedIndex(in.ManifestOutput, raw); err != nil {
		return err
	}

	if sink != nil {
		if err := sink.Set(ctx, "image-ref", ref); err != nil {
			return err
		}

		return sink.Set(ctx, "image-digest", strings.TrimPrefix(ref, derived.ImageRepository+"@"))
	}

	return nil
}

func verifyIndexContent(ctx context.Context, registry RawManifestRegistry, ref string, derived signerImageManifestDerived) ([]byte, error) {
	if !strings.HasPrefix(ref, derived.ImageRepository+"@") {
		return nil, fmt.Errorf("index reference is outside the expected repository: %w", errs.ErrValidation)
	}

	raw, err := registry.Manifest(ctx, ref)
	if err != nil {
		return nil, err
	}

	digest, err := digestRawManifest(raw, ref)
	if err != nil {
		return nil, err
	}

	if ref != derived.ImageRepository+"@"+digest {
		return nil, fmt.Errorf("registry manifest does not match the requested digest: %w", errs.ErrValidation)
	}

	expected := make([]domaincontainer.PlatformDigest, 0, len(derived.Archs))
	for index, arch := range derived.Archs {
		expected = append(expected, domaincontainer.PlatformDigest{Platform: "linux/" + arch, Digest: "sha256:" + derived.Digests[index]})
	}

	if err := domaincontainer.ValidateImageIndex(raw, expected); err != nil {
		return nil, err
	}

	return raw, nil
}

func writeVerifiedIndex(path string, raw []byte) error {
	if path == "" {
		return nil
	}

	return cliio.WriteFile(path, raw, 0o644)
}

func validateIndexSnapshotPath(path, authFile string, derived signerImageManifestDerived) error {
	if path == "" {
		return nil
	}

	if path == cliio.StdSentinel {
		return fmt.Errorf("manifest-output requires a file; stdout (-) is not supported: %w", errs.ErrUsage)
	}

	if err := requireSingleLine(path, "manifest-output"); err != nil {
		return err
	}

	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("manifest-output must be a regular file, not a directory or link: %w", errs.ErrUsage)
	}

	protected := make([]string, 0, len(derived.Archs)+3)
	protected = append(protected, authFile, derived.MetadataDir, filepath.Join(derived.MetadataDir, derived.Name+".json"))

	for _, arch := range derived.Archs {
		protected = append(protected, filepath.Join(derived.Name+"-arch-"+arch, derived.Name+"-"+arch+".json"))
	}

	return refuseSnapshotAliases(path, protected)
}

func refuseSnapshotAliases(output string, protected []string) error {
	absoluteOutput, err := canonicalSnapshotPath(output)
	if err != nil {
		return err
	}

	outputInfo, _ := os.Stat(output)

	for _, path := range protected {
		absolutePath, absErr := canonicalSnapshotPath(path)
		if absErr != nil {
			return absErr
		}

		info, _ := os.Stat(path)
		if absoluteOutput == absolutePath || (info != nil && outputInfo != nil && os.SameFile(info, outputInfo)) {
			return fmt.Errorf("manifest-output must not overwrite authentication or image metadata: %w", errs.ErrUsage)
		}
	}

	return nil
}

// Resolve existing components with the standard library and retain a missing
// suffix: the generated metadata file may not exist yet. A dangling link is
// not a missing directory; refusing it avoids aliases that become live when
// the assembly job creates its output directory. This is a static preflight,
// not protection against another process changing the workspace during a run.
func canonicalSnapshotPath(path string) (string, error) {
	candidate, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	suffix := ""

	for {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr == nil {
			return filepath.Join(resolved, suffix), nil
		}

		if !errors.Is(resolveErr, fs.ErrNotExist) {
			return "", resolveErr
		}

		info, statErr := os.Lstat(candidate)
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return "", statErr
		}

		if statErr == nil && info.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("snapshot path contains an unresolved link: %w", errs.ErrUsage)
		}

		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", resolveErr
		}

		suffix = filepath.Join(filepath.Base(candidate), suffix)
		candidate = parent
	}
}
