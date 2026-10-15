// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

// stubResolver maps refs → digests.
type stubResolver map[string]string

func (s stubResolver) ResolveDigest(_ context.Context, ref string) (string, error) {
	d, ok := s[ref]
	if !ok {
		return "", errs.ErrMissingInput
	}

	return d, nil
}

// recordingRegistry is a Registry that records the copies it was asked to
// perform without changing what the resolver answers.
type recordingRegistry struct {
	stubResolver

	copied []string
}

func (r *recordingRegistry) CopyTag(_ context.Context, source, dest string) error {
	r.copied = append(r.copied, source+"->"+dest)

	return nil
}

func TestPromoteLedger_ReportsTagsActuallyCopied(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		name              string
		candidate, moving bool
		want              int
	}{
		{name: "no candidate"},
		{name: "final already present", candidate: true},
		{name: "only moving tag copied", candidate: true, moving: true, want: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

			entry := imageledger.Entry{Ref: "127.0.0.1:1/app@" + digest, Digest: digest, FinalTag: "127.0.0.1:1/app:v1.2.3"}
			if scenario.candidate {
				entry.CandidateTag = "127.0.0.1:1/app:staging-v1.2.3"
			}

			if scenario.moving {
				entry.MovingTag = "127.0.0.1:1/app:stable"
			}

			reg := &recordingRegistry{stubResolver: stubResolver{entry.CandidateTag: digest, entry.FinalTag: digest, entry.MovingTag: digest}}

			var out bytes.Buffer

			err := PromoteLedger(t.Context(), LedgerPromotion{
				Registry: reg, Entries: []imageledger.Entry{entry}, ReleaseTag: "v1.2.3",
				Stage: imageledger.Stage{UseEntryReleaseTags: true}, ErrPrefix: "ledger", Out: &out,
			})

			want := fmt.Sprintf("ledger: promoted %d tag(s) to stage %q\n", scenario.want, "release")
			if err != nil || out.String() != want || len(reg.copied) != scenario.want {
				t.Fatalf("err=%v copied=%v output=%q, want %q", err, reg.copied, out.String(), want)
			}
		})
	}
}

func TestWritePromotionJournal_ReusesMatchingOriginalState(t *testing.T) {
	t.Parallel()

	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	entry := imageledger.Entry{
		Ref:          "codeberg.org/itiquette/repo@" + digest,
		Digest:       digest,
		FinalTag:     "codeberg.org/itiquette/repo:v1.2.3",
		MovingTag:    "codeberg.org/itiquette/repo:stable",
		CandidateTag: "codeberg.org/itiquette/repo:staging-v1.2.3",
	}
	journal := filepath.Join(t.TempDir(), "promotion.jsonl")
	resolver := stubResolver{entry.CandidateTag: digest}
	run := LedgerPromotion{
		Registry:       &recordingRegistry{stubResolver: resolver},
		Entries:        []imageledger.Entry{entry},
		ReleaseTag:     "v1.2.3",
		Stage:          imageledger.Stage{Name: "release", UseEntryReleaseTags: true},
		Journal:        journal,
		JournalDirPerm: 0o700,
		ErrPrefix:      "test",
	}

	if err := writePromotionJournal(context.Background(), run); err != nil {
		t.Fatalf("write initial journal: %v", err)
	}

	original, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}

	// Model a retry after a partial promotion. Replanning now would record the
	// promoted state and destroy the rollback evidence; reuse must keep the
	// original bytes instead.
	resolver[entry.FinalTag] = digest
	resolver[entry.MovingTag] = digest

	if writeErr := writePromotionJournal(context.Background(), run); writeErr != nil {
		t.Fatalf("reuse matching journal: %v", writeErr)
	}

	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(after, original) {
		t.Fatalf("retry rewrote original rollback evidence\nbefore: %s\nafter:  %s", original, after)
	}
}

func TestWritePromotionJournal_RefusesMismatchedExistingJournal(t *testing.T) {
	t.Parallel()

	const (
		digest      = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		otherDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	)

	entry := imageledger.Entry{
		Ref:          "codeberg.org/itiquette/repo@" + digest,
		Digest:       digest,
		FinalTag:     "codeberg.org/itiquette/repo:v1.2.3",
		CandidateTag: "codeberg.org/itiquette/repo:staging-v1.2.3",
	}
	journal := filepath.Join(t.TempDir(), "promotion.jsonl")
	run := LedgerPromotion{
		Registry:       &recordingRegistry{stubResolver: stubResolver{entry.CandidateTag: digest}},
		Entries:        []imageledger.Entry{entry},
		ReleaseTag:     "v1.2.3",
		Stage:          imageledger.Stage{Name: "release", UseEntryReleaseTags: true},
		Journal:        journal,
		JournalDirPerm: 0o700,
		ErrPrefix:      "test",
	}

	if err := writePromotionJournal(context.Background(), run); err != nil {
		t.Fatalf("write initial journal: %v", err)
	}

	original, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}

	run.Entries[0].Digest = otherDigest
	run.Entries[0].Ref = "codeberg.org/itiquette/repo@" + otherDigest

	if writeErr := writePromotionJournal(context.Background(), run); !errors.Is(writeErr, errs.ErrValidation) {
		t.Fatalf("mismatched journal must be refused, got %v", writeErr)
	}

	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(after, original) {
		t.Fatalf("mismatched retry changed journal\nbefore: %s\nafter:  %s", original, after)
	}
}

// A dry run leaves no journal: rollback deletes the tags a journal names, and a
// preview's journal would name tags no promotion created.
func TestPromoteLedger_DryRunWritesNoJournal(t *testing.T) {
	t.Parallel()

	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	entry := imageledger.Entry{
		Ref:          "codeberg.org/itiquette/repo@" + digest,
		Digest:       digest,
		FinalTag:     "codeberg.org/itiquette/repo:v1.2.3",
		CandidateTag: "codeberg.org/itiquette/repo:staging-v1.2.3",
	}
	journal := filepath.Join(t.TempDir(), "promotion.jsonl")

	var out bytes.Buffer

	// The registry answers as the command's simulated one does after a copy.
	err := PromoteLedger(t.Context(), LedgerPromotion{
		Registry:       &recordingRegistry{stubResolver: stubResolver{entry.CandidateTag: digest, entry.FinalTag: digest}},
		Entries:        []imageledger.Entry{entry},
		ReleaseTag:     "v1.2.3",
		Stage:          imageledger.Stage{Name: "release", UseEntryReleaseTags: true},
		Journal:        journal,
		JournalDirPerm: 0o700,
		DryRun:         true,
		ErrPrefix:      "test",
		Out:            &out,
	})
	if err != nil || out.String() != "test: would promote 0 tag(s) to stage \"release\"\n" {
		t.Fatalf("dry-run promotion: %v, output %q", err, out.String())
	}

	if _, statErr := os.Stat(journal); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a dry run left a promotion journal behind: %v", statErr)
	}
}

// failingCopyRegistry resolves digests normally but refuses every copy,
// modelling a promotion that dies after planning and before any tag lands.
type failingCopyRegistry struct {
	stubResolver
}

func (failingCopyRegistry) CopyTag(context.Context, string, string) error {
	return errs.ErrValidation // any error: the copy refusing is the point
}

// TestRunLedgerPromotion_JournalsBeforeTheFirstCopy pins the crash-safety
// ordering the promotion body promises: the rollback journal is written before
// PromoteToStage moves any tag. The blackbox suite cannot prove this — failing
// the copy from outside also fails journal planning, because both resolve the
// candidate — so the ordering is pinned here, where the copy can fail alone.
func TestRunLedgerPromotion_JournalsBeforeTheFirstCopy(t *testing.T) {
	t.Parallel()

	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	entry := imageledger.Entry{
		Ref:          "codeberg.org/itiquette/repo@" + digest,
		Digest:       digest,
		FinalTag:     "codeberg.org/itiquette/repo:v1.2.3",
		MovingTag:    "codeberg.org/itiquette/repo:stable",
		CandidateTag: "codeberg.org/itiquette/repo:staging-v1.2.3",
	}
	journal := filepath.Join(t.TempDir(), "promotion.jsonl")
	run := LedgerPromotion{
		Registry:       failingCopyRegistry{stubResolver{entry.CandidateTag: digest}},
		Entries:        []imageledger.Entry{entry},
		ReleaseTag:     "v1.2.3",
		Stage:          imageledger.Stage{Name: "release", UseEntryReleaseTags: true},
		Journal:        journal,
		JournalDirPerm: 0o700,
		ErrPrefix:      "test",
	}

	err := PromoteLedger(context.Background(), run)
	if err == nil {
		t.Fatal("promotion succeeded although every copy failed")
	}

	body, readErr := os.ReadFile(journal)
	if readErr != nil {
		t.Fatalf("journal missing after a failed promotion — it must be written before the first copy "+
			"so a promotion that dies midway can be rolled back: %v", readErr)
	}

	if !bytes.Contains(body, []byte(digest)) {
		t.Fatalf("journal does not record the planned digest: %s", body)
	}
}
