// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages_test

import (
	"github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPromotionImagesFromLedgerPreservesReviewedIdentities(t *testing.T) {
	t.Parallel()

	entry := signLedgerEntry()
	entry.CandidateTag = signRepository + ":staging-" + entry.BaseInputID + "-alpine"
	body, err := imageledger.Marshal([]imageledger.Entry{entry})
	require.NoError(t, err)
	images, err := baseimages.PromotionImagesFromLedger(body, signRepository)
	require.NoError(t, err)
	require.Len(t, images, 1)
	require.Equal(t, entry.FinalTag, images[0].Tag)
	require.Equal(t, entry.Ref, images[0].Ref)
	require.Equal(t, entry.Ref, images[0].CandidateRef)
	require.Equal(t, entry.CandidateTag, images[0].CandidateTag)
	require.Equal(t, entry.BaseInputID, images[0].BaseInputID)

	_, err = baseimages.PromotionImagesFromLedger(body, "registry.example/other/project")
	require.Error(t, err)
	_, err = baseimages.PromotionImagesFromLedger([]byte("[]"), signRepository)
	require.Error(t, err)
}
