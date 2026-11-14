// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
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

func TestBaseInputsFromLedgerProjectsDeclaredLineage(t *testing.T) {
	t.Parallel()

	root := signLedgerEntry()
	child := signLedgerEntry()
	child.Flavor = "child"
	child.BaseInputID = strings.Repeat("d", 64)
	child.FinalTag = signRepository + ":" + child.BaseInputID + "-child"
	parent := map[string]any{"ref": root.Ref, "input_id": root.BaseInputID}
	child.Provenance = map[string]any{"parent_base": parent}

	body, err := imageledger.Marshal([]imageledger.Entry{root, child})
	require.NoError(t, err)
	inputs, err := baseimages.BaseInputsFromLedger(body, signRepository)
	require.NoError(t, err)
	require.Equal(t, []baseimages.BaseInput{
		{Flavor: root.Flavor, BaseInputID: root.BaseInputID},
		{Flavor: "child", BaseInputID: child.BaseInputID, ParentBase: &baseimages.BaseImageParent{Ref: root.Ref, InputID: root.BaseInputID}},
	}, inputs)

	for name, bad := range map[string]any{
		"extra field":  map[string]any{"ref": root.Ref, "input_id": root.BaseInputID, "flavor": "x"},
		"mutable ref":  map[string]any{"ref": signRepository + ":latest", "input_id": root.BaseInputID},
		"not a parent": "parent",
	} {
		child.Provenance = map[string]any{"parent_base": bad}
		body, err := imageledger.Marshal([]imageledger.Entry{root, child})
		require.NoError(t, err, name)
		_, err = baseimages.BaseInputsFromLedger(body, signRepository)
		require.ErrorIs(t, err, errs.ErrValidation, name)
	}
}
