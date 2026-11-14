// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

func TestPrereleaseCannotPromoteStageAliases(t *testing.T) {
	for _, stage := range []string{"", "dev", "staging", "release"} {
		t.Run(stage, func(t *testing.T) {
			journal := filepath.Join(t.TempDir(), "promotion.jsonl")

			err := appcontainer.PromoteLedger(t.Context(), appcontainer.LedgerPromotion{ReleaseTag: "v1.0.0-beta.2", Stage: imageledger.Stage{Name: stage}, Journal: journal})
			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("beta stage promotion allowed: %v", err)
			}

			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Fatalf("journal written before refusal: %v", err)
			}
		})
	}
}
