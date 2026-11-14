// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// Each family with a native version tool is named once, in family order,
// whatever order and number its projects come in; text-bumped types and a
// plan that does not bump name none.
func TestVersionFamilies_NamesEachNativeFamilyOnce(t *testing.T) {
	t.Parallel()

	items := []pipeline.PlannedArtifact{
		{Name: "crate", ProjectType: projecttype.Cargo}, {Name: "app", ProjectType: projecttype.Gradle},
		{Name: "api", ProjectType: projecttype.Maven}, {Name: "lib", ProjectType: projecttype.Maven},
		{Name: "cli", ProjectType: projecttype.Go},
	}

	plan := pipeline.ReleasePrepareStagePlan{Targets: pipeline.ReleasePrepareTargets{
		VersionBump: pipeline.TargetPlan[pipeline.PlannedArtifact]{Runs: true, Items: items},
	}}

	want := []pipeline.VersionFamily{{Family: "java", ProjectType: projecttype.Maven}, {Family: "rust", ProjectType: projecttype.Cargo}}
	if got := plan.VersionFamilies(); !slices.Equal(got, want) {
		t.Errorf("VersionFamilies() = %v, want %v", got, want)
	}

	plan.Targets.VersionBump.Runs = false
	if got := plan.VersionFamilies(); len(got) != 0 {
		t.Errorf("a plan that does not bump names %v", got)
	}
}
