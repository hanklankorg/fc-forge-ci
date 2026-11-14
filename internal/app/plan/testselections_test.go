// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"strings"
	"testing"

	appplan "github.com/diggsweden/forge-ci/internal/app/plan"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

const selectionBuildPlan = `{"version":1,"stage":"build","targets":{"go":{"runs":true,"items":[{"name":"cli","project_type":"go","go":{}}]},"cargo":{"runs":true,"items":[{"name":"rust","project_type":"cargo","cargo":{}}]}}}`

func TestValidateReleaseSelectionsRejectsAmbiguousPlanBeforeOutputs(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(selectionBuildPlan, `"targets":`, `"targets":{},"targets":`, 1),
		strings.Replace(selectionBuildPlan, `"name":"cli"`, `"name":"other","name":"cli"`, 1),
		strings.Replace(selectionBuildPlan, `"project_type":"go"`, `"project_type":"cargo","project_type":"go"`, 1),
	} {
		sink := fakeoutputsink.New(t)
		if err := appplan.ValidateReleaseTestSelections(t.Context(), sink, raw, "{}", "{}"); err == nil || len(sink.Order()) != 0 {
			t.Fatalf("ambiguous provider matrix accepted: %v", err)
		}
	}
}

func TestValidateReleaseSelectionsEmitsNothingOnLateMapFailure(t *testing.T) {
	sink := fakeoutputsink.New(t)

	err := appplan.ValidateReleaseTestSelections(t.Context(), sink, selectionBuildPlan, `{"cli":{"packages":["./..."],"gate":"policy"}}`, `{"rust":false}`)
	if err == nil || len(sink.Order()) != 0 {
		t.Fatalf("late failure emitted a usable partial mapping: %v %+v", err, sink.Order())
	}
}

func TestValidateReleaseSelectionsCanonicalizesLegacyEmptyMaps(t *testing.T) {
	sink := fakeoutputsink.New(t)
	if err := appplan.ValidateReleaseTestSelections(t.Context(), sink, selectionBuildPlan, "  ", ""); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"go-test-selections-json", "cargo-test-selections-json"} {
		if sink.Single(key) != "{}" {
			t.Fatalf("legacy mapping not canonical: %q", sink.Single(key))
		}
	}
}
