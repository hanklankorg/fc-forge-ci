// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

const otherDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

// fakeCleanupRegistry resolves digests and records/applies deletes.
type fakeCleanupRegistry struct {
	digests map[string]string
	deleted []string
	delErr  error
}

func (r *fakeCleanupRegistry) ResolveDigest(_ context.Context, ref string) (string, error) {
	d, ok := r.digests[ref]
	if !ok {
		return "", errs.ErrMissingInput
	}

	return d, nil
}

func (r *fakeCleanupRegistry) DeleteTag(_ context.Context, ref string) error {
	if r.delErr != nil {
		return r.delErr
	}

	r.deleted = append(r.deleted, ref)
	delete(r.digests, ref)

	return nil
}

func TestCleanup_DeletesCandidateWhenFinalVerified(t *testing.T) {
	t.Parallel()

	e := candidateEntry()
	reg := &fakeCleanupRegistry{digests: map[string]string{
		e.FinalTag:     goodDigest, // promoted final serves the digest
		e.CandidateTag: goodDigest,
	}}

	if result, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3"); err != nil || result.Deleted != 1 {
		t.Fatalf("cleanup failed: %v", err)
	}

	if !slices.Equal(reg.deleted, []string{e.CandidateTag}) {
		t.Errorf("expected candidate deleted, got %v", reg.deleted)
	}
}

func TestCleanup_LeavesCandidateWhenFinalMissing(t *testing.T) {
	t.Parallel()

	e := candidateEntry()
	reg := &fakeCleanupRegistry{digests: map[string]string{e.CandidateTag: goodDigest}}

	if result, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3"); err != nil || result.Deleted != 0 {
		t.Fatalf("cleanup should preserve candidate when the promoted final tag is missing: %v", err)
	}

	if len(reg.deleted) != 0 {
		t.Errorf("candidate must be left in place when final is missing, got deleted=%v", reg.deleted)
	}
}

func TestCleanup_FailsWhenFinalDigestMismatches(t *testing.T) {
	t.Parallel()

	e := candidateEntry()
	reg := &fakeCleanupRegistry{digests: map[string]string{
		e.FinalTag:     otherDigest,
		e.CandidateTag: goodDigest,
	}}

	if _, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3"); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("cleanup mismatch error = %v, want ErrValidation", err)
	}

	if len(reg.deleted) != 0 {
		t.Errorf("candidate must be left in place on final digest mismatch, got deleted=%v", reg.deleted)
	}
}

func TestCleanup_LeavesCandidateWhenMovingTagMissing(t *testing.T) {
	t.Parallel()

	e := candidateEntry()
	e.MovingTag = "codeberg.org/itiquette/gommitlint:rust"
	reg := &fakeCleanupRegistry{digests: map[string]string{
		e.FinalTag:     goodDigest,
		e.CandidateTag: goodDigest,
	}}

	if result, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3"); err != nil || result.Deleted != 0 {
		t.Fatalf("cleanup should preserve candidate when moving_tag is missing: %v", err)
	}

	if len(reg.deleted) != 0 {
		t.Errorf("candidate must be left in place when moving_tag is missing, got deleted=%v", reg.deleted)
	}

	reg.digests[e.MovingTag] = goodDigest
	if result, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3"); err != nil || result.Deleted != 1 {
		t.Fatalf("cleanup with verified moving tag failed: %v", err)
	}

	if !slices.Equal(reg.deleted, []string{e.CandidateTag}) {
		t.Errorf("expected candidate deleted after moving_tag verifies, got %v", reg.deleted)
	}
}

func TestCleanup_FailsWhenCandidateDigestMismatches(t *testing.T) {
	t.Parallel()

	e := candidateEntry()
	reg := &fakeCleanupRegistry{digests: map[string]string{
		e.FinalTag:     goodDigest,
		e.CandidateTag: otherDigest,
	}}

	if _, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3"); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("cleanup mismatch error = %v, want ErrValidation", err)
	}

	if len(reg.deleted) != 0 {
		t.Errorf("candidate must be left in place on candidate digest mismatch, got deleted=%v", reg.deleted)
	}
}

func TestCleanup_FailsWhenMovingTagDigestMismatches(t *testing.T) {
	t.Parallel()

	e := candidateEntry()
	e.MovingTag = "codeberg.org/itiquette/gommitlint:rust"
	reg := &fakeCleanupRegistry{digests: map[string]string{
		e.FinalTag:     goodDigest,
		e.CandidateTag: goodDigest,
		e.MovingTag:    otherDigest,
	}}

	if _, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3"); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("cleanup mismatch error = %v, want ErrValidation", err)
	}

	if len(reg.deleted) != 0 {
		t.Errorf("candidate must be left in place on moving_tag digest mismatch, got deleted=%v", reg.deleted)
	}
}

func TestCleanup_SkipsEntryWithoutCandidate(t *testing.T) {
	t.Parallel()

	e := validEntry() // no candidate
	reg := &fakeCleanupRegistry{digests: map[string]string{}}

	if result, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3"); err != nil || result.Deleted != 0 {
		t.Fatalf("entry without candidate should be skipped, got %v", err)
	}

	if len(reg.deleted) != 0 {
		t.Errorf("nothing should be deleted, got %v", reg.deleted)
	}
}

func TestCleanup_DoesNotDeleteSignatureArtifactsWhenFinalVerified(t *testing.T) {
	t.Parallel()

	e := candidateEntry()
	reg := &fakeCleanupRegistry{digests: map[string]string{
		e.FinalTag:     goodDigest,
		e.CandidateTag: goodDigest,
	}}

	if result, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3"); err != nil || result.Deleted != 1 {
		t.Fatalf("cleanup failed: %v", err)
	}

	if !slices.Equal(reg.deleted, []string{e.CandidateTag}) {
		t.Errorf("cleanup should delete only the staging tag, not sha256-*.sig/.att package versions; deleted=%v", reg.deleted)
	}
}

// TestCleanup_VisitsEveryEntry covers the loop and its skip. All the
// tests above pass one entry, so a Cleanup that stopped after the first
// would satisfy every one of them while leaving the candidate tags of
// every other image behind -- which is exactly the litter this command
// exists to remove.
//
// It also pins that an entry without a candidate tag is skipped rather
// than treated as an error: nothing to clean up is not a failure.
func TestCleanup_VisitsEveryEntry(t *testing.T) {
	t.Parallel()

	first := validEntry()
	first.CandidateTag = "codeberg.org/itiquette/gommitlint:staging-v1.2.3"

	second := validEntry()
	second.Role = "static"
	second.Ref = "codeberg.org/itiquette/gommitlint-static@" + goodDigest
	second.FinalTag = "codeberg.org/itiquette/gommitlint-static:v1.2.3"
	second.CandidateTag = "codeberg.org/itiquette/gommitlint-static:staging-v1.2.3"

	// No candidate tag: nothing to clean up, and not an error.
	noCandidate := validEntry()
	noCandidate.Role = "debug"
	noCandidate.Ref = "codeberg.org/itiquette/gommitlint-debug@" + goodDigest
	noCandidate.FinalTag = "codeberg.org/itiquette/gommitlint-debug:v1.2.3"

	reg := &fakeCleanupRegistry{digests: map[string]string{
		first.CandidateTag:   goodDigest,
		first.FinalTag:       goodDigest,
		second.CandidateTag:  goodDigest,
		second.FinalTag:      goodDigest,
		noCandidate.FinalTag: goodDigest,
	}}

	if result, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{first, noCandidate, second}, "v1.2.3"); err != nil || result.Deleted != 2 {
		t.Fatalf("Cleanup: %v", err)
	}

	// Both candidates gone, in entry order, and nothing else touched --
	// only the candidate tag may be deleted, never a final tag.
	want := []string{first.CandidateTag, second.CandidateTag}
	if !slices.Equal(reg.deleted, want) {
		t.Errorf("deleted = %v, want %v", reg.deleted, want)
	}
}

// droppingRegistry is a fakeCleanupRegistry whose delete also removes a
// sibling tag, the way a manifest-level delete takes every name of the digest.
type droppingRegistry struct {
	fakeCleanupRegistry
	alsoDrops string
}

func (r *droppingRegistry) DeleteTag(ctx context.Context, ref string) error {
	if err := r.fakeCleanupRegistry.DeleteTag(ctx, ref); err != nil {
		return err
	}

	delete(r.digests, r.alsoDrops)

	return nil
}

// TestCleanup_RerunDeletesNothingAndADisturbingDeleteIsReported pins the two
// halves of the safety contract no earlier test reached: a rerun whose
// candidate is already gone deletes nothing, and a delete that disturbs the
// promoted final or moving tag is reported after the fact rather than
// swallowed.
func TestCleanup_RerunDeletesNothingAndADisturbingDeleteIsReported(t *testing.T) {
	t.Parallel()

	e := candidateEntry()
	e.MovingTag = "codeberg.org/itiquette/gommitlint:rust"

	rerun := &fakeCleanupRegistry{digests: map[string]string{e.FinalTag: goodDigest, e.MovingTag: goodDigest}}
	if result, err := imageledger.Cleanup(context.Background(), rerun, []imageledger.Entry{e}, "v1.2.3"); err != nil || result.Deleted != 0 || len(rerun.deleted) != 0 {
		t.Fatalf("a rerun with the candidate already gone: err=%v deleted=%v, want nothing", err, rerun.deleted)
	}

	for _, disturbed := range []string{e.FinalTag, e.MovingTag} {
		reg := &droppingRegistry{
			fakeCleanupRegistry: fakeCleanupRegistry{digests: map[string]string{e.FinalTag: goodDigest, e.MovingTag: goodDigest, e.CandidateTag: goodDigest}},
			alsoDrops:           disturbed,
		}

		_, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3")
		if err == nil || !strings.Contains(err.Error(), "after candidate cleanup") || !strings.Contains(err.Error(), disturbed) {
			t.Errorf("a delete that dropped %s: err = %v, want the post-delete refusal naming it", disturbed, err)
		}
	}
}

// A registry that does not delete tags at all, such as GHCR, keeps the
// candidate: cleanup reports it and goes on, since the release is complete.
func TestCleanup_KeepsWhatTheRegistryCannotDelete(t *testing.T) {
	t.Parallel()

	e := candidateEntry()
	reg := &fakeCleanupRegistry{digests: map[string]string{
		e.FinalTag:     goodDigest,
		e.CandidateTag: goodDigest,
	}, delErr: errs.ErrUnsupported}

	result, err := imageledger.Cleanup(context.Background(), reg, []imageledger.Entry{e}, "v1.2.3")
	if err != nil || result.Deleted != 0 || len(result.Left) != 1 || result.Left[0] != e.CandidateTag {
		t.Fatalf("Cleanup = %+v, %v; want the candidate kept and reported", result, err)
	}
}
