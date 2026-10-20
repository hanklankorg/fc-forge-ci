// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

func TestImagePathExtractionSurvivesConfigPlanning(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse([]byte(`sign: {method: sigstore, transparency: public}
artifacts:
  - name: source
    project-type: cargo
    config: {build-mode: container-first}
containers:
  - name: runtime
    from: [source]
    platforms: linux/amd64
    extract:
      binary:
        paths: [/worker, /usr/local/bin/keytool]
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}
	plan := pipeline.NewConfigPlan(cfg)
	container := plan.Containers.All[0]
	if container.ExtractBinaryTarget != "" || !slices.Equal(container.ExtractBinaryPaths, []string{"/worker", "/usr/local/bin/keytool"}) || !slices.Equal(container.ExtractBinaryNames, []string{"worker", "keytool"}) {
		t.Fatalf("image extraction was dropped or renamed: %+v", container)
	}
	cfg.Containers[0].Extract.Binary.Paths[0] = "/changed-after-plan"
	if container.ExtractBinaryPaths[0] != "/worker" {
		t.Fatal("plan aliases mutable config paths")
	}
	release, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{ConfigPlan: plan, Branch: "main", RefName: "v1.2.3", ReleasePublisher: "github-cli", ChangelogCreator: "git-cliff", ReleaseSBOMs: "none", ReleaseSignArtifacts: true})
	if err != nil {
		t.Fatal(err)
	}
	transfers := 0
	for _, item := range release.ArtifactTransfers.Items {
		if item.Kind == pipeline.ArtifactTransferExtractedBinaries {
			transfers++
			if item.Name != "runtime-binaries-amd64" {
				t.Fatalf("wrong image-file handoff: %+v", item)
			}
		}
	}
	if transfers != 1 {
		t.Fatalf("path-based extraction produced %d release transfers, want one", transfers)
	}
}
