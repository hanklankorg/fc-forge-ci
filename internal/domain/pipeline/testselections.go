// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// DecodeReleaseTestSelectionPlan also refuses duplicate consumed plan members:
// the Go preflight and the provider's matrix must see the same artifact set.
func DecodeReleaseTestSelectionPlan(raw string) (ReleaseBuildStagePlan, error) {
	var plan ReleaseBuildStagePlan
	if err := DecodeStagePlan(raw, "release build-stage plan", &plan); err != nil {
		return plan, err
	}
	if err := checkConfigPlanMembers(json.RawMessage(raw), reflect.TypeFor[ReleaseBuildStagePlan](), "build-stage-plan"); err != nil {
		return plan, err
	}
	return plan, nil
}

// ReleaseTestSelections validates the whole administrator mapping before fanout.
// Blank omission retains the legacy default. Every supplied map is exhaustive for
// its running ecosystem and cannot contain stale, extra or differently typed keys.
func ReleaseTestSelections(plan ReleaseBuildStagePlan, goJSON, cargoJSON string) (map[string]build.GoTestSelection, map[string]build.CargoTestSelection, error) {
	if plan.Version != ReleasePlanVersion || plan.Stage != "build" {
		return nil, nil, fmt.Errorf("invalid release build-stage contract: %w", errs.ErrInvalidConfig)
	}
	seen := map[string]bool{}
	for name, target := range map[string]TargetPlan[PlannedArtifact]{
		"go": plan.Targets.Go, "cargo": plan.Targets.Cargo, "maven": plan.Targets.Maven,
		"npm": plan.Targets.NPM, "gradle": plan.Targets.Gradle,
		"gradle-android": plan.Targets.GradleAndroid, "xcode-ios": plan.Targets.XcodeIOS,
	} {
		if target.Runs != (len(target.Items) > 0) {
			return nil, nil, fmt.Errorf("incoherent %s build target: %w", name, errs.ErrInvalidConfig)
		}
		for _, artifact := range target.Items {
			if artifact.Name == "" || artifact.Name != strings.TrimSpace(artifact.Name) || seen[artifact.Name] || string(artifact.ProjectType) != name {
				return nil, nil, fmt.Errorf("invalid, duplicate or wrongly typed build artifact %q in %s: %w", artifact.Name, name, errs.ErrInvalidConfig)
			}
			if name == "go" && (artifact.Go == nil || artifact.Cargo != nil) || name == "cargo" && (artifact.Cargo == nil || artifact.Go != nil) {
				return nil, nil, fmt.Errorf("missing or conflicting native config for %q: %w", artifact.Name, errs.ErrInvalidConfig)
			}
			seen[artifact.Name] = true
		}
	}
	goSelections, err := releaseSelectionMap(goJSON, plan.Targets.Go, build.ParseGoTestSelection)
	if err != nil {
		return nil, nil, fmt.Errorf("validate Go test selections: %w", err)
	}
	cargoSelections, err := releaseSelectionMap(cargoJSON, plan.Targets.Cargo, build.ParseCargoTestSelection)
	if err != nil {
		return nil, nil, fmt.Errorf("validate Cargo test selections: %w", err)
	}
	return goSelections, cargoSelections, nil
}

func releaseSelectionMap[T any](raw string, target TargetPlan[PlannedArtifact], parse func(string) (T, error)) (map[string]T, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]T{}, nil
	}
	body := bytes.TrimSpace([]byte(raw))
	if !json.Valid(body) || len(body) == 0 || body[0] != '{' {
		return nil, fmt.Errorf("expected one JSON object keyed by selected artifact names: %w", errs.ErrUsage)
	}
	// Reuse the existing contract member walk: do not let JSON overwrite a
	// duplicate map key, gate or target field before the typed parser sees it.
	if err := checkConfigPlanMembers(body, reflect.TypeFor[map[string]T](), "test-selections"); err != nil {
		return nil, err
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, err
	}
	result := map[string]T{}
	expected := map[string]bool{}
	for _, artifact := range target.Items {
		expected[artifact.Name] = true
	}
	for name, entry := range entries {
		if !expected[name] {
			return nil, fmt.Errorf("unknown or unselected artifact %q: %w", name, errs.ErrUsage)
		}
		selection, err := parse(string(entry))
		if err != nil {
			return nil, fmt.Errorf("artifact %q: %w", name, err)
		}
		result[name] = selection
	}
	for name := range expected {
		if _, ok := result[name]; !ok {
			return nil, fmt.Errorf("missing artifact %q: %w", name, errs.ErrUsage)
		}
	}
	return result, nil
}
