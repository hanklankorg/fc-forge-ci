// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

func TestRepositoryBindings_CheckLaterEntries(t *testing.T) {
	t.Parallel()

	const repo = "registry.example/app"

	good := imageledger.Entry{Ref: repo + "@sha256:" + strings.Repeat("a", 64), FinalTag: repo + ":v1.2.3"}
	bad := good
	bad.FinalTag = "registry.example/other:v1.2.3"

	require.NoError(t, imageledger.ValidateEntryRepositories([]imageledger.Entry{good, good}, repo))
	err := imageledger.ValidateEntryRepositories([]imageledger.Entry{good, bad}, repo)
	require.ErrorIs(t, err, errs.ErrValidation)
	require.Contains(t, err.Error(), "entry 1")

	first := imageledger.PromotionRecord{SourceRef: good.Ref, FinalTag: good.FinalTag}
	second := first
	second.FinalTag = bad.FinalTag

	require.NoError(t, imageledger.ValidatePromotionRecordRepositories([]imageledger.PromotionRecord{first, first}, repo))
	err = imageledger.ValidatePromotionRecordRepositories([]imageledger.PromotionRecord{first, second}, repo)
	require.ErrorIs(t, err, errs.ErrValidation)
	require.Contains(t, err.Error(), "entry 1")
}
