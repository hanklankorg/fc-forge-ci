// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainsbom "github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// Validate all evidence destinations before scanners or registry writes. An
// index BOM is optional for existing callers; requesting it requires inventories
// and the exact served-index snapshot, never a new registry pull or rescan.
func validateStagedIndexEvidence(in SignerImageAssembleInput, derived signerImageManifestDerived, inputs []LayoutRecordInput) error {
	evidence := in.Evidence

	platforms := make([]string, 0, len(inputs))
	for _, input := range inputs {
		platforms = append(platforms, input.Platform)
	}

	parsed, err := parseImageEvidencePlatforms(platforms)
	if err != nil {
		return err
	}

	paths := []string{in.ManifestOutput, evidence.IndexSBOMOutput}
	for _, platform := range parsed {
		paths = append(paths, renderImageEvidenceTemplate(evidence.TrivyOutputTemplate, platform), renderImageEvidenceTemplate(evidence.SBOMOutputTemplate, platform))
	}

	if err := validateImageEvidenceOutputPaths(paths); err != nil {
		return err
	}

	if err := refuseStagedEvidenceOverwrite(in, derived, inputs, paths); err != nil {
		return err
	}

	if evidence.IndexSBOMOutput == "" && evidence.SBOMURLPrefix == "" {
		return nil
	}

	if evidence.IndexSBOMOutput == "" || evidence.SBOMOutputTemplate == "" || in.ManifestOutput == "" || evidence.SBOMURLPrefix == "" {
		return fmt.Errorf("index SBOM requires inventories, manifest output and a public SBOM URL prefix: %w", errs.ErrUsage)
	}

	// Public release downloads flatten inventory paths to their filenames.
	// Distinct local directories must not hide a collision until after upload.
	names := make(map[string]bool, len(parsed))
	for _, platform := range parsed {
		name := filepath.Base(renderImageEvidenceTemplate(evidence.SBOMOutputTemplate, platform))
		if names[name] {
			return fmt.Errorf("platform inventory filenames must be distinct in public SBOM URLs: %w", errs.ErrUsage)
		}

		names[name] = true
	}

	return domainsbom.ValidatePublicBOMURL(evidence.SBOMURLPrefix)
}

func refuseStagedEvidenceOverwrite(in SignerImageAssembleInput, derived signerImageManifestDerived, inputs []LayoutRecordInput, paths []string) error {
	for _, path := range paths {
		if err := validateIndexSnapshotPath(path, in.AuthFile, derived); err != nil {
			return err
		}

		if path == "" {
			continue
		}

		output, err := canonicalSnapshotPath(path)
		if err != nil {
			return err
		}

		for _, input := range inputs {
			source, err := canonicalSnapshotPath(filepath.Dir(input.Directory))
			if err != nil {
				return err
			}

			if output == source || strings.HasPrefix(output, source+string(filepath.Separator)) {
				return fmt.Errorf("image evidence must not overwrite staged image inputs: %w", errs.ErrUsage)
			}
		}
	}

	return nil
}

func writeStagedIndexEvidence(ctx context.Context, sink ci.OutputSink, in SignerImageAssembleInput, inputs []LayoutRecordInput, meta *SignerImageMetadata) error {
	evidence := in.Evidence

	index, err := readImageEvidenceReport(in.ManifestOutput)
	if err != nil {
		return err
	}

	parts := make([]domainsbom.IndexBOMPart, 0, len(inputs))
	for _, input := range inputs {
		platforms, err := parseImageEvidencePlatforms([]string{input.Platform})
		if err != nil {
			return err
		}

		path := renderImageEvidenceTemplate(evidence.SBOMOutputTemplate, platforms[0])

		document, err := readImageEvidenceReport(path)
		if err != nil {
			return err
		}

		record, err := readImageLayoutRecord(input.Directory)
		if err != nil {
			return err
		}

		repository, _, _ := strings.Cut(meta.Ref, "@")
		parts = append(parts, domainsbom.IndexBOMPart{
			Platform: input.Platform, ImageRef: repository + "@" + record.Digest,
			URL:    strings.TrimRight(evidence.SBOMURLPrefix, "/") + "/" + url.PathEscape(filepath.Base(path)),
			SHA256: fmt.Sprintf("%x", sha256.Sum256(document)), Document: document,
		})
	}

	body, err := domainsbom.ComposeIndexBOM(meta.Ref, index, parts)
	if err != nil {
		return err
	}

	stage, err := pathsafe.NewArtifactStaging(filepath.Dir(evidence.IndexSBOMOutput))
	if err != nil {
		return err
	}

	defer func() { _ = stage.Close() }()

	if err := stage.Root().WriteFile(filepath.Base(evidence.IndexSBOMOutput), body, 0o644); err != nil {
		return err
	}

	if err := stage.Install(); err != nil {
		return err
	}

	if sink != nil {
		return sink.Set(ctx, "image-sbom-sha256", fmt.Sprintf("%x", sha256.Sum256(body)))
	}

	return nil
}
