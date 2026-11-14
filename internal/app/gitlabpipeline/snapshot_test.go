// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabpipeline_test

import (
	"github.com/diggsweden/forge-ci/internal/app/gitlabpipeline"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/version"
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

// A file output is one job that builds in its family's image and keeps its
// bundle as its own artifact, under the plan's name and for the documented
// retention. A plan without an image renders no image job beside it.
func TestSnapshotPipelineKeepsFileOutputsAsJobArtifacts(t *testing.T) {
	t.Parallel()

	image := "registry.example/engine@sha256:" + strings.Repeat("b", 64)
	goImage := "registry.example/go@sha256:" + strings.Repeat("c", 64)
	in := gitlabpipeline.SnapshotPipelineOptions{Project: "org/engine", Pin: strings.Repeat("a", 40), Pipeline: "42", JobImage: image, BuildImage: image, AMD64Tags: []string{"amd64"}, Images: gitlabpipeline.RuntimeImages{Go: goImage}}

	tag, err := version.ComposeSnapshotContainerTag("1.2.3", "main", "aaaaaaa")
	require.NoError(t, err)

	plan := snapshotimage.Plan{BaseVersion: "1.2.3", SourceBranch: "main", Tag: tag, Files: []pipeline.PlannedArtifact{{Name: "cli", ProjectType: projecttype.Go}}}

	out, err := gitlabpipeline.SnapshotPipeline(plan, in)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"extends": ".forge-ci-snapshot-files", "stage": "artifacts", "tags": []string{"amd64"},
		"image":     map[string]any{"name": goImage, "entrypoint": []string{""}},
		"script":    []string{"snapshot_gitlab_files 'cli'"},
		"needs":     []any{map[string]any{"pipeline": "42", "job": "Snapshot: plan", "artifacts": true}},
		"artifacts": map[string]any{"name": "cli-1.2.3-snapshot-main-aaaaaaa", "paths": []string{"snapshot-files/cli/"}, "expire_in": "7 days"},
	}, out["Snapshot: files cli"])

	for name := range out {
		require.NotContains(t, name, "Snapshot: build")
		require.NotContains(t, name, "Snapshot: publish")
		require.NotContains(t, name, "Snapshot: context")
	}

	// GitLab expands variables in an artifact's name.
	plan.BaseVersion = "$CI_JOB_TOKEN"
	_, err = gitlabpipeline.SnapshotPipeline(plan, in)
	require.ErrorContains(t, err, "invalid snapshot file output name")

	plan.BaseVersion, plan.Files[0].ProjectType = "1.2.3", projecttype.XcodeIOS
	_, err = gitlabpipeline.SnapshotPipeline(plan, in)
	require.ErrorContains(t, err, "unsupported snapshot artifact family")
}
