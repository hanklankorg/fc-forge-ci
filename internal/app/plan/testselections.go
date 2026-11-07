// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan

import (
	"context"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// ValidateReleaseTestSelections emits only wholly validated administrator maps;
// no output can be consumed by matrix leaves after partial validation.
func ValidateReleaseTestSelections(ctx context.Context, sink ci.OutputSink, planJSON, goJSON, cargoJSON string) error {
	plan, err := pipeline.DecodeReleaseTestSelectionPlan(planJSON)
	if err != nil {
		return err
	}
	goSelections, cargoSelections, err := pipeline.ReleaseTestSelections(plan, goJSON, cargoJSON)
	if err != nil {
		return err
	}
	if err := emitJSONOutput(ctx, sink, "go-test-selections-json", goSelections); err != nil {
		return err
	}
	return emitJSONOutput(ctx, sink, "cargo-test-selections-json", cargoSelections)
}
