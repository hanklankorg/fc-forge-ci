// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabpipeline_test

import (
	"github.com/diggsweden/forge-ci/internal/app/gitlabpipeline"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestSnapshotPipelineRejectsInvalidGraphsBeforeRendering(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"mutable-pin", "mutable-image", "unknown-builder", "unknown-architecture", "unknown-image", "missing-build", "duplicate-build", "unsafe-name", "kaniko-image-mismatch"} {
		in := gitlabpipeline.SnapshotPipelineOptions{Project: "org/engine", Pin: strings.Repeat("a", 40), Pipeline: "42", JobImage: "registry.example/engine@sha256:" + strings.Repeat("b", 64), BuildImage: "registry.example/engine@sha256:" + strings.Repeat("b", 64)}
		plan := snapshotimage.Plan{Images: []snapshotimage.Image{{Name: "api"}}, Builds: []snapshotimage.Build{{Name: "api", Arch: "amd64"}}}

		switch scenario {
		case "mutable-pin":
			in.Pin = "main"
		case "mutable-image":
			in.JobImage = "registry.example/engine:latest"
		case "unknown-builder":
			in.Builder = "docker"
		case "unknown-architecture":
			plan.Builds[0].Arch = "386"
		case "unknown-image":
			plan.Builds[0].Name = "other"
		case "missing-build":
			plan.Builds = nil
		case "duplicate-build":
			plan.Builds = append(plan.Builds, plan.Builds[0])
		case "unsafe-name":
			plan.Images[0].Name = "$(touch marker)"
		case "kaniko-image-mismatch":
			in.Builder = "kaniko"
		}

		out, err := gitlabpipeline.SnapshotPipeline(plan, in)
		require.Error(t, err, scenario)
		require.Nil(t, out)
	}
}
