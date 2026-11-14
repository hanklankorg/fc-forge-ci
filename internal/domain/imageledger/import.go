// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger

import (
	"context"
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// PlanReleaseImport rehomes a confined release ledger to one exact publication
// repository. Only image coordinates change; build dependencies, SBOM identities
// and source provenance remain the producer's. It publishes no final tags.
func PlanReleaseImport(entries []Entry, releaseTag, sourceRepository, destinationRepository string) ([]Entry, error) {
	if err := validateImportRepositories(sourceRepository, destinationRepository); err != nil {
		return nil, err
	}

	if err := ValidateAll(entries, releaseTag); err != nil {
		return nil, err
	}

	if err := ValidateEntryRepositories(entries, sourceRepository); err != nil {
		return nil, err
	}

	result := make([]Entry, 0, len(entries))
	owners := make(map[string]int)

	for index, entry := range entries {
		if entry.ImageKind == ImageKindBase {
			return nil, fmt.Errorf("imageledger: import accepts release images, not base-image publication: %w", errs.ErrValidation)
		}

		imported := entry
		imported.Ref = destinationRepository + "@" + entry.Digest
		imported.FinalTag = destinationRepository + ":" + tagName(entry.FinalTag)

		candidate := StagingTagPrefix + tagName(entry.FinalTag)
		if entry.CandidateTag != "" {
			candidate = tagName(entry.CandidateTag)
		}

		imported.CandidateTag = destinationRepository + ":" + candidate
		if entry.MovingTag != "" {
			imported.MovingTag = destinationRepository + ":" + tagName(entry.MovingTag)
		}

		if err := imported.Validate(releaseTag); err != nil {
			return nil, err
		}

		if err := admitImportTargets(imported, index, owners); err != nil {
			return nil, err
		}

		result = append(result, imported)
	}

	return result, nil
}

func validateImportRepositories(sourceRepository, destinationRepository string) error {
	source, err := canonicalPromotionTarget(sourceRepository + ":import-probe")
	if err != nil {
		return err
	}

	destination, err := canonicalPromotionTarget(destinationRepository + ":import-probe")
	if err != nil {
		return err
	}

	if source == destination || container.StripTagOrDigest(sourceRepository) != sourceRepository ||
		container.StripTagOrDigest(destinationRepository) != destinationRepository {
		return fmt.Errorf("imageledger: import needs distinct untagged source and destination repositories: %w", errs.ErrValidation)
	}

	return nil
}

func admitImportTargets(entry Entry, index int, owners map[string]int) error {
	for _, target := range []string{entry.CandidateTag, entry.FinalTag, entry.MovingTag} {
		if target == "" {
			continue
		}

		canonical, err := canonicalPromotionTarget(target)
		if err != nil {
			return err
		}

		if previous, exists := owners[canonical]; exists && previous != index {
			return fmt.Errorf("imageledger: entries %d and %d target the same imported tag: %w", previous, index, errs.ErrValidation)
		}

		owners[canonical] = index
	}

	return nil
}

// ImportReleaseImages copies immutable source digests to destination candidates
// without copying producer-supplied signatures. A complete verified ledger is
// returned only after every copy succeeds. Matching candidates are reusable;
// conflicting candidates are never replaced.
func ImportReleaseImages(ctx context.Context, registry Registry, entries []Entry, releaseTag, sourceRepository, destinationRepository string) ([]Entry, error) {
	imported, err := PlanReleaseImport(entries, releaseTag, sourceRepository, destinationRepository)
	if err != nil {
		return nil, err
	}

	return copyImportedImages(ctx, registry, entries, imported)
}

// copyImportedImages publishes only admitted candidate coordinates and checks
// the served bytes. The release and base planners keep their own admission rules.
func copyImportedImages(ctx context.Context, registry Registry, entries, imported []Entry) ([]Entry, error) {
	for index, entry := range imported {
		ready, err := immutableFinalAlreadyServesDigest(ctx, registry, entry.CandidateTag, entry.Digest)
		if err != nil {
			return nil, err
		}

		if ready {
			continue
		}

		if err := registry.CopyTag(ctx, entries[index].Ref, entry.CandidateTag); err != nil {
			return nil, err
		}

		if err := verifyRefDigest(ctx, registry, entry.CandidateTag, entry.Digest); err != nil {
			return nil, err
		}
	}

	return imported, nil
}
