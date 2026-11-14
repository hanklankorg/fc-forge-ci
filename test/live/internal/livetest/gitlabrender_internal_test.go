// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Use the live gate renderer and each demo's actual declaration/scripts. The
// shared checker expands the component jobs and release child before checking
// structure; provider scheduling, credentials and dynamic rules remain live proof.
func TestGitLabDemosStructurallyRenderForBothBuildersAndEvents(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	demos := 0

	for _, entry := range reporoot.ReadDir(t, "test/live/fixtures/consumers") {
		if !entry.IsDir() {
			continue
		}

		consumer := filepath.Join("test/live/fixtures/consumers", entry.Name(), "consumer")

		gate, err := reporoot.ReadFileAt(root, filepath.Join(consumer, GitLabGateFile))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			t.Fatal(err)
		}

		demos++

		for _, builder := range []string{ImageBuilderBuildah, ImageBuilderKaniko} {
			t.Run(entry.Name()+"/"+builder, func(t *testing.T) {
				t.Parallel()

				layer := Layer{ServerURL: "https://gitlab.example", Repository: "forgeci/engine", admin: testWorkflowAdmin(t)}
				layer.Commit = layer.admin.commit
				images := GitLabGateImages{
					Job:  "registry.invalid/runtime@sha256:" + strings.Repeat("a", 64),
					Lint: "registry.invalid/linter@sha256:" + strings.Repeat("b", 64), ExtraJobs: map[string]string{},
				}

				names, err := gitlabGateExtraJobImages(gate)
				if err != nil {
					t.Fatal(err)
				}

				for _, name := range names {
					images.ExtraJobs[name] = "registry.invalid/runtime-" + strings.ToLower(name) + "@sha256:" + strings.Repeat("c", 64)
				}

				t.Run(TrialPullRequest, func(t *testing.T) {
					policy, err := renderGitLabAdminPolicy(gate, layer, images)
					if err != nil {
						t.Fatal(err)
					}

					for _, problem := range gitlabrender.DocumentProblems(t, consumer+"/administrator-MR-policy", []byte(policy)) {
						t.Error(problem)
					}
				})

				t.Run(TrialRelease, func(t *testing.T) {
					if _, err := reporoot.ReadFileAt(root, filepath.Join(consumer, GitLabConsumerBuildScript)); errors.Is(err, fs.ErrNotExist) {
						t.Skip("this demo does not serve a native GitLab release")
					} else if err != nil {
						t.Fatal(err)
					}

					releaseImages, declaration, err := GitLabReleaseImages(filepath.Join(root, consumer))
					if err != nil {
						t.Fatal(err)
					}

					exports, err := GitLabConsumerExportCommands(filepath.Join(root, consumer), nil)
					if err != nil {
						t.Fatal(err)
					}

					exportInputs := make([]any, 0, len(exports))
					for _, command := range exports {
						exportInputs = append(exportInputs, command)
					}

					inputs := map[string]any{
						"image-builder": builder, "builder-job-image": "registry.invalid/" + builder + "@sha256:" + strings.Repeat("d", 64),
						"job-image": images.Job, "job-user": "65532:65532",
						"runtime-image-java": images.Job, "runtime-image-node": images.Job, "runtime-image-go": images.Job,
						"runtime-image-rust": images.Job, "runtime-image-android": images.Job,
						"checks": []any{"bash .gitlab/check-release.sh"}, "build": []any{"bash " + GitLabConsumerBuildScript},
						"release-images": releaseImages, "artifacts-config": declaration, "release-platforms": "linux/amd64",
						"export-files": len(exports) != 0, "export": exportInputs,
					}
					if builder == ImageBuilderBuildah {
						inputs["build-runner-tags-linux-amd64"] = []any{"privileged"}
					}

					for _, problem := range gitlabrender.Problems(t, "templates/release.yml", inputs) {
						t.Error(problem)
					}
				})
			})
		}
	}

	if demos == 0 {
		t.Fatal("no GitLab demo was structurally rendered")
	}
}

func TestCargoContainerDemoEnablesItsNativeExportJobs(t *testing.T) {
	t.Parallel()

	commands, err := GitLabConsumerExportCommands(filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-cargo-container/consumer"), nil)
	if err != nil || len(commands) != 1 || commands[0] != "bash "+GitLabConsumerExportScript {
		t.Fatalf("native demo export was omitted: commands=%v err=%v", commands, err)
	}
}
