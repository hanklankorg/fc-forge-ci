// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger

import (
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ValidateEntryRepository ensures every image ref/tag on an entry stays inside
// the expected repository. Empty expectedRepository disables the check so the
// generic ledger remains registry-agnostic unless a forge boundary opts in.
func ValidateEntryRepository(entry Entry, expectedRepository string) error {
	if expectedRepository == "" {
		return nil
	}

	for _, ref := range []struct {
		field string
		value string
	}{
		{"ref", entry.Ref},
		{finalTagField, entry.FinalTag},
		{movingTagField, entry.MovingTag},
		{candidateTagField, entry.CandidateTag},
	} {
		if err := ValidateRefUnderRepository(ref.field, ref.value, expectedRepository); err != nil {
			return err
		}
	}

	return nil
}

// ValidateEntryRepositories applies ValidateEntryRepository to every entry and
// names the first one that leaves the expected repository.
func ValidateEntryRepositories(entries []Entry, expectedRepository string) error {
	for idx, entry := range entries {
		if err := ValidateEntryRepository(entry, expectedRepository); err != nil {
			return fmt.Errorf("imageledger: entry %d: %w", idx, err)
		}
	}

	return nil
}

// ValidatePromotionRecordRepository applies the same repository constraint to a
// rollback journal record. Journals are a signer-boundary artifact, so the
// rollback command rechecks them before deleting/restoring tags.
func ValidatePromotionRecordRepository(record PromotionRecord, expectedRepository string) error {
	if expectedRepository == "" {
		return nil
	}

	for _, ref := range []struct {
		field string
		value string
	}{
		{"source_ref", record.SourceRef},
		{finalTagField, record.FinalTag},
		{movingTagField, record.MovingTag},
		{candidateTagField, record.CandidateTag},
	} {
		if err := ValidateRefUnderRepository(ref.field, ref.value, expectedRepository); err != nil {
			return err
		}
	}

	return nil
}

// ValidatePromotionRecordRepositories applies ValidatePromotionRecordRepository
// to every journal record and names the first one that leaves the expected
// repository.
func ValidatePromotionRecordRepositories(records []PromotionRecord, expectedRepository string) error {
	for idx, record := range records {
		if err := ValidatePromotionRecordRepository(record, expectedRepository); err != nil {
			return fmt.Errorf("imageledger: promotion journal entry %d: %w", idx, err)
		}
	}

	return nil
}

// ValidateRefUnderRepository reports a validation error when ref is set and does
// not resolve to expectedRepository (after stripping any tag/digest). An empty
// ref or empty expectedRepository disables the check, keeping the generic ledger
// registry-agnostic unless a forge boundary opts in. It is the single source of
// truth for the per-field repository constraint shared by the ledger and the
// container ledger-sign path.
func ValidateRefUnderRepository(field, ref, expectedRepository string) error {
	if ref == "" || expectedRepository == "" {
		return nil
	}

	if got := container.StripTagOrDigest(ref); got != expectedRepository {
		return fmt.Errorf("imageledger: %s must be under %s: %q: %w", field, expectedRepository, ref, errs.ErrValidation)
	}

	return nil
}
