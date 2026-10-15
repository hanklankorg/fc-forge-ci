// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger

import (
	"context"
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// PlanBaseImport admits a nonempty, base-only ledger and rehomes only its image
// coordinates. Content identities, SBOM pins and build lineage stay unchanged.
// Base publication has no release-tag namespace or moving-tag authority.
func PlanBaseImport(entries []Entry, sourceRepository, destinationRepository string) ([]Entry, error) {
	if err := admitBaseImport(entries, sourceRepository, destinationRepository); err != nil {
		return nil, err
	}

	imported := make([]Entry, 0, len(entries))
	owners := make(map[string]int)

	for index, entry := range entries {
		item := entry
		item.Ref = destinationRepository + "@" + entry.Digest
		item.FinalTag = destinationRepository + ":" + tagName(entry.FinalTag)

		item.CandidateTag = destinationRepository + ":" + StagingTagPrefix + tagName(entry.FinalTag)
		if err := item.Validate(""); err != nil {
			return nil, err
		}

		if err := admitImportTargets(item, index, owners); err != nil {
			return nil, err
		}

		imported = append(imported, item)
	}

	return imported, nil
}

func admitBaseImport(entries []Entry, sourceRepository, destinationRepository string) error {
	if err := validateImportRepositories(sourceRepository, destinationRepository); err != nil {
		return err
	}

	if len(entries) == 0 {
		return fmt.Errorf("imageledger: base import needs at least one entry: %w", errs.ErrValidation)
	}

	for index, entry := range entries {
		if err := validateBaseImportIdentity(entry, sourceRepository); err != nil {
			return fmt.Errorf("imageledger: base import entry %d: %w", index, err)
		}
	}

	if err := ValidateAll(entries, ""); err != nil {
		return err
	}

	return ValidateEntryRepositories(entries, sourceRepository)
}

func validateBaseImportIdentity(entry Entry, sourceRepository string) error {
	if entry.ImageKind != ImageKindBase || !container.ValidSHA256Hex(entry.BaseInputID) || entry.Flavor == "" ||
		entry.FinalTag != sourceRepository+":"+entry.BaseInputID+"-"+entry.Flavor || entry.MovingTag != "" ||
		entry.Ref != sourceRepository+"@"+entry.Digest {
		return fmt.Errorf("must bind one content-addressed base identity: %w", errs.ErrValidation)
	}

	return nil
}

// ImportBaseImages copies admitted source digests to immutable publication
// candidates, without signatures or final tags. Conflicting candidates refuse.
func ImportBaseImages(ctx context.Context, registry Registry, entries []Entry, sourceRepository, destinationRepository string) ([]Entry, error) {
	imported, err := PlanBaseImport(entries, sourceRepository, destinationRepository)
	if err != nil {
		return nil, err
	}

	return copyImportedImages(ctx, registry, entries, imported)
}
