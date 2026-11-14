// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger_test

import (
	"context"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/stretchr/testify/require"
)

func TestPromotionSource_CandidateRetagCannotChangeTheCopiedImage(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"", "mirror.invalid/releases"} {
		t.Run("target="+target, func(t *testing.T) {
			t.Parallel()

			const repository = "registry.invalid:5000/team/app"

			entry := imageledger.Entry{
				// A different ref repository is valid in the generic model. The
				// admitted candidate, not this unused fallback, owns the source.
				Ref: "registry.invalid/other/app@" + goodDigest, Digest: goodDigest,
				CandidateTag: repository + ":staging-v1.2.3", FinalTag: repository + ":v1.2.3", MovingTag: repository + ":release",
			}
			stage := imageledger.Stage{Name: "release", UseEntryReleaseTags: true, TargetRepo: target}
			reg := &retaggingPromotionRegistry{
				candidate: entry.CandidateTag,
				tags:      map[string]string{entry.CandidateTag: goodDigest},
				manifests: map[string]string{repository + "@" + goodDigest: goodDigest, repository + "@" + otherDigest: otherDigest},
			}

			records, err := imageledger.PlanReleasePromotionRollback(t.Context(), reg, []imageledger.Entry{entry}, "v1.2.3", stage)
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.Equal(t, entry.CandidateTag+"@"+goodDigest, records[0].SourceRef, "preserve the existing journal representation")

			encoded, err := imageledger.MarshalPromotionJournal(records)
			require.NoError(t, err)
			reloaded, err := imageledger.ParsePromotionJournal(encoded)
			require.NoError(t, err)
			require.NoError(t, imageledger.ValidatePromotionJournalIntent(reloaded, []imageledger.Entry{entry}, "v1.2.3", stage))

			reg.retagAfterCheck = true
			copied, err := imageledger.PromoteToStage(t.Context(), reg, reg, []imageledger.Entry{entry}, "v1.2.3", stage)
			require.NoError(t, err)
			require.Equal(t, 2, copied)
			require.False(t, reg.retagAfterCheck, "the candidate must still be admitted before copying")
			require.Equal(t, otherDigest, reg.tags[entry.CandidateTag], "the candidate actually moved during promotion")
			require.Equal(t, goodDigest, reg.tags[records[0].FinalTag])
			require.Equal(t, goodDigest, reg.tags[records[0].MovingTag])
			require.Equal(t, []string{records[0].SourceRef, records[0].SourceRef}, reg.sources)
			require.Equal(t, []bool{target != "", target != ""}, reg.withSignatures)
			require.Equal(t, records, reloaded, "promotion must not change recorded rollback intent")
		})
	}
}

type retaggingPromotionRegistry struct {
	candidate       string
	tags            map[string]string
	manifests       map[string]string
	retagAfterCheck bool
	sources         []string
	withSignatures  []bool
}

func (r *retaggingPromotionRegistry) ResolveDigest(_ context.Context, ref string) (string, error) {
	digest, err := r.currentDigest(ref)
	if err == nil && ref == r.candidate && r.retagAfterCheck {
		r.tags[ref] = otherDigest
		r.retagAfterCheck = false
	}

	return digest, err
}

func (r *retaggingPromotionRegistry) CopyTag(_ context.Context, source, dest string) error {
	return r.copy(source, dest, false)
}

func (r *retaggingPromotionRegistry) CopyWithSignatures(_ context.Context, source, dest string) error {
	return r.copy(source, dest, true)
}

func (r *retaggingPromotionRegistry) currentDigest(ref string) (string, error) {
	if _, digest, pinned := strings.Cut(ref, "@"); pinned {
		if got, exists := r.manifests[container.StripTagOrDigest(ref)+"@"+digest]; exists {
			return got, nil
		}
	} else if got, exists := r.tags[ref]; exists {
		return got, nil
	}

	return "", errs.ErrMissingInput
}

func (r *retaggingPromotionRegistry) copy(source, dest string, signatures bool) error {
	digest, err := r.currentDigest(source)
	if err != nil {
		return err
	}

	r.tags[dest] = digest
	r.sources = append(r.sources, source)
	r.withSignatures = append(r.withSignatures, signatures)

	return nil
}
