// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"encoding/json"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

func TestPrereleaseImagePlanDropsMovingAliases(t *testing.T) {
	in := releaseImagesInput()
	in.ReleaseTag = "v1.0.0-beta.2"

	plan, err := pipeline.PlanReleaseImages(plannedContainers(t, "  - name: app\n"), in)
	if err != nil {
		t.Fatal(err)
	}

	plan.Images[0].MovingTagName = "latest"

	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}

	got, err := pipeline.ReadImageBuildPlan(string(body), in.ImageRepository, in.ReleaseTag)
	if err != nil {
		t.Fatal(err)
	}

	if got.Images[0].MovingTagName != "" || got.Images[0].FinalTagName != in.ReleaseTag {
		t.Fatalf("unsafe beta destinations: %+v", got.Images[0])
	}
}
