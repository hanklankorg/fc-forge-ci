// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

func promotionTargetEntry(repository, flavor, digest string) imageledger.Entry {
	return imageledger.Entry{
		Ref: repository + "@" + digest, Digest: digest,
		FinalTag: repository + ":v1.2.3-" + flavor, MovingTag: repository + ":stable",
		CandidateTag: repository + ":staging-v1.2.3-" + flavor,
	}
}

func TestPromotionTargets_RejectInvalidComposedReferences(t *testing.T) {
	t.Parallel()

	first := promotionTargetEntry("127.0.0.1:1/org/app", "go", goodDigest)
	second := promotionTargetEntry("127.0.0.1:1/org/"+strings.Repeat("a", 150), "rust", otherDigest)
	entries := []imageledger.Entry{first, second}

	for _, prefix := range []string{"registry", "127.0.0.1:1/team/../other", "127.0.0.1:1/team/.", "127.0.0.1:1/" + strings.Repeat("p", 130)} {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()

			// Every source is valid; the invalid reference only appears after
			// composing the destination prefix with its source repository path.
			if err := imageledger.ValidateAll(entries, "v1.2.3"); err != nil {
				t.Fatal(err)
			}

			t.Run("promote", func(t *testing.T) {
				t.Parallel()

				reg := &fakeRegistry{digests: map[string]string{first.CandidateTag: first.Digest, second.CandidateTag: second.Digest}}
				signatures := &fakeSigCopier{reg: reg}

				copied, err := imageledger.PromoteToStage(t.Context(), reg, signatures, entries, "", imageledger.Stage{Name: "dev", TargetRepo: prefix})
				if !errors.Is(err, errs.ErrValidation) || copied != 0 || len(reg.copies) != 0 || len(signatures.copies) != 0 {
					t.Fatalf("invalid target: copied=%d err=%v writes=%v signature copies=%v", copied, err, reg.copies, signatures.copies)
				}
			})

			t.Run("journal", func(t *testing.T) {
				t.Parallel()

				reg := &fakePromotionRollbackRegistry{digests: map[string]string{first.CandidateTag: first.Digest, second.CandidateTag: second.Digest}}
				stage := imageledger.Stage{UseEntryReleaseTags: true, TargetRepo: prefix}

				records, err := imageledger.PlanReleasePromotionRollback(t.Context(), reg, entries, "v1.2.3", stage)
				if !errors.Is(err, errs.ErrValidation) || len(records) != 0 || len(reg.events) != 0 {
					t.Fatalf("invalid target produced a journal plan or contacted the registry: records=%v err=%v events=%v", records, err, reg.events)
				}
			})
		})
	}
}

func TestPromotionTargets_RegistryAliasesCannotOwnTheSameTag(t *testing.T) {
	t.Parallel()

	first := promotionTargetEntry("docker.io/review/app", "go", goodDigest)
	second := promotionTargetEntry("index.docker.io/review/app", "rust", otherDigest)
	entries := []imageledger.Entry{first, second}
	stage := imageledger.Stage{UseEntryReleaseTags: true}

	if err := imageledger.ValidateAll(entries, "v1.2.3"); err != nil {
		t.Fatal(err)
	}

	t.Run("promote", func(t *testing.T) {
		t.Parallel()

		reg := &fakeRegistry{digests: map[string]string{first.CandidateTag: first.Digest, second.CandidateTag: second.Digest}}

		copied, err := imageledger.PromoteToStage(t.Context(), reg, nil, entries, "v1.2.3", stage)
		if !errors.Is(err, errs.ErrValidation) || copied != 0 || len(reg.copies) != 0 {
			t.Fatalf("aliased target accepted: copied=%d err=%v writes=%v", copied, err, reg.copies)
		}
	})

	t.Run("plan journal", func(t *testing.T) {
		t.Parallel()

		reg := &fakePromotionRollbackRegistry{digests: map[string]string{first.CandidateTag: first.Digest, second.CandidateTag: second.Digest}}

		records, err := imageledger.PlanReleasePromotionRollback(t.Context(), reg, entries, "v1.2.3", stage)
		if !errors.Is(err, errs.ErrValidation) || len(records) != 0 || len(reg.events) != 0 {
			t.Fatalf("aliased targets reached journal planning: records=%v err=%v events=%v", records, err, reg.events)
		}
	})

	records := make([]imageledger.PromotionRecord, 0, len(entries))
	for _, entry := range entries {
		records = append(records, imageledger.PromotionRecord{
			Version: imageledger.PromotionJournalVersion, SourceRef: entry.CandidateTag + "@" + entry.Digest,
			CandidateTag: entry.CandidateTag, FinalTag: entry.FinalTag, MovingTag: entry.MovingTag,
		})
	}

	t.Run("reuse journal", func(t *testing.T) {
		t.Parallel()

		if err := imageledger.ValidatePromotionJournalIntent(records, entries, "v1.2.3", stage); !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("aliased journal intent accepted: %v", err)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		t.Parallel()

		reg := &fakePromotionRollbackRegistry{digests: map[string]string{
			first.FinalTag: first.Digest, first.MovingTag: first.Digest,
			second.FinalTag: second.Digest, second.MovingTag: second.Digest,
		}}

		if err := imageledger.RollbackReleasePromotion(t.Context(), reg, records, "v1.2.3"); !errors.Is(err, errs.ErrValidation) || len(reg.events) != 0 {
			t.Fatalf("aliased rollback targets accepted: err=%v events=%v", err, reg.events)
		}
	})
}
