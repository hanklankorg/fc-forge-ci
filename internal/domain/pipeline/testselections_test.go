// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
)

const goTestMap = `{"g1":{"packages":["./internal/..."],"gate":"policy"},"g2":{"packages":["./cmd/..."],"gate":"application"}}`
const cargoTestMap = `{"c1":{"runs":[{"kind":"package-test","package":"example","name":"guard","gate":"policy"}]}}`

func nativeSelectionPlan() ReleaseBuildStagePlan {
	return ReleaseBuildStagePlan{Version: ReleasePlanVersion, Stage: "build", Targets: ReleaseBuildTargets{
		Go: TargetPlan[PlannedArtifact]{Runs: true, Items: []PlannedArtifact{
			{Name: "g1", ProjectType: "go", Go: &config.GoConfig{}},
			{Name: "g2", ProjectType: "go", Go: &config.GoConfig{}},
		}},
		Cargo: TargetPlan[PlannedArtifact]{Runs: true, Items: []PlannedArtifact{{Name: "c1", ProjectType: "cargo", Cargo: &config.CargoConfig{}}}},
	}}
}

func TestReleaseSelectionMapsRejectWholeObjectAndKeyErrors(t *testing.T) {
	for _, tc := range []struct{ name, goJSON, cargoJSON string }{
		{"explicit empty Go", "{}", cargoTestMap},
		{"explicit empty Cargo", goTestMap, " { } "},
		{"Go null run", `{"g1":{"packages":["./..."],"gate":"policy","run":null},"g2":{"packages":["./..."],"gate":"policy"}}`, cargoTestMap},
		{"cargo Unicode duplicate", goTestMap, `{"c1":{"runs":[{"kind":"package-test","package":"app","name":"guard","gate":"policy"}],"runſ":[{"kind":"package-bin","package":"app","name":"main","gate":"application"}]}}`},
		{"null", "null", cargoTestMap},
		{"false", "false", cargoTestMap},
		{"array", "[]", cargoTestMap},
		{"scalar", `"{}"`, cargoTestMap},
		{"trailing", goTestMap + ` {}`, cargoTestMap},
		{"missing", `{"g1":{"packages":["./..."]}}`, cargoTestMap},
		{"extra", `{"g1":{"packages":["./..."]},"g2":{"packages":["./..."]},"typo":{"packages":["./..."]}}`, cargoTestMap},
		{"wrong ecosystem key", `{"c1":{"packages":["./..."]}}`, cargoTestMap},
		{"duplicate key", `{"g1":{"packages":["./..."]},"g1":{"packages":["./cmd/..."]},"g2":{"packages":["./..."]}}`, cargoTestMap},
		{"duplicate entry field", `{"g1":{"packages":["./..."],"packages":[]},"g2":{"packages":["./..."]}}`, cargoTestMap},
		{"null entry", `{"g1":null,"g2":{"packages":["./..."]}}`, cargoTestMap},
		{"false entry", `{"g1":false,"g2":{"packages":["./..."]}}`, cargoTestMap},
		{"empty entry", `{"g1":{},"g2":{"packages":["./..."]}}`, cargoTestMap},
		{"empty string entry", `{"g1":"","g2":{"packages":["./..."]}}`, cargoTestMap},
		{"wrong entry type", `{"g1":{"runs":[]},"g2":{"packages":["./..."]}}`, cargoTestMap},
		{"cargo null", goTestMap, `{"c1":null}`},
		{"cargo false", goTestMap, `{"c1":false}`},
		{"cargo empty", goTestMap, `{"c1":{}}`},
		{"cargo extra", goTestMap, `{"wrong":{"runs":[]}}`},
		{"cargo duplicate gate", goTestMap, `{"c1":{"runs":[{"kind":"package-test","package":"example","name":"guard","gate":"policy","gate":"application"}]}}`},
		{"cargo group", goTestMap, `{"c1":{"runs":[{"kind":"workspace-bins","gate":"application"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ReleaseTestSelections(nativeSelectionPlan(), tc.goJSON, tc.cargoJSON); err == nil {
				t.Fatal("invalid whole mapping accepted")
			}
		})
	}
}

func TestReleaseSelectionMapsValidateSelectedArtifactCoherence(t *testing.T) {
	for _, mutate := range []func(*ReleaseBuildStagePlan){
		func(p *ReleaseBuildStagePlan) { p.Targets.Go.Items[0].ProjectType = "cargo" },
		func(p *ReleaseBuildStagePlan) { p.Targets.Go.Items[0].Go = nil },
		func(p *ReleaseBuildStagePlan) { p.Targets.Cargo.Items[0].Cargo = nil },
		func(p *ReleaseBuildStagePlan) { p.Targets.Go.Runs = false },
		func(p *ReleaseBuildStagePlan) { p.Targets.Cargo.Items = nil },
		func(p *ReleaseBuildStagePlan) { p.Targets.Go.Items[1].Name = "g1" },
		func(p *ReleaseBuildStagePlan) { p.Targets.Cargo.Items[0].Name = "g1" },
		func(p *ReleaseBuildStagePlan) { p.Stage = "publish" },
	} {
		plan := nativeSelectionPlan()
		mutate(&plan)
		if _, _, err := ReleaseTestSelections(plan, goTestMap, cargoTestMap); err == nil {
			t.Fatalf("incoherent artifact plan accepted: %+v", plan)
		}
	}
	plan := nativeSelectionPlan()
	plan.Targets.Cargo = TargetPlan[PlannedArtifact]{}
	if _, _, err := ReleaseTestSelections(plan, goTestMap, cargoTestMap); err == nil {
		t.Fatal("unselected artifact key accepted")
	}
}

func TestReleaseSelectionMapsPreserveLegacyEmptyAndValidateExplicitMaps(t *testing.T) {
	if _, _, err := ReleaseTestSelections(ReleaseBuildStagePlan{Version: ReleasePlanVersion, Stage: "build"}, "{}", "{}"); err != nil {
		t.Fatalf("empty maps without native artifacts rejected: %v", err)
	}
	for _, empty := range []string{"", "  "} {
		goSelections, cargoSelections, err := ReleaseTestSelections(nativeSelectionPlan(), empty, empty)
		if err != nil || len(goSelections) != 0 || len(cargoSelections) != 0 {
			t.Fatalf("legacy empty behavior changed: %v", err)
		}
	}
	goSelections, cargoSelections, err := ReleaseTestSelections(nativeSelectionPlan(), goTestMap, cargoTestMap)
	if err != nil || len(goSelections) != 2 || len(cargoSelections) != 1 {
		t.Fatalf("valid exhaustive maps rejected: %v", err)
	}
}
