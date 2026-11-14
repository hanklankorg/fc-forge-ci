// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"strings"
	"testing"
)

// registryInputWorkflows are the workflows that fetch a pinned registry input
// in a job of its own and build from it in another: the download of the
// fetched layouts in the build job, and the build that consumes them.
var registryInputWorkflows = []struct{ file, download, build string }{
	{releaseImagesWorkflow, `-inputs-${IMAGE_NAME:?}-${IMAGE_ARCH:?}`, "release-images build-prepared"},
	{baseImagesWorkflow, `-inputs-${IMAGE_NAME:?}-${IMAGE_ARCH:?}`, "container base-images child-base build"},
}

const (
	releaseImagesWorkflow = ".forgejo/workflows/release-images.yml"
	baseImagesWorkflow    = ".forgejo/workflows/base-images.yml"
)

func TestRegistryInputFetchSerializesArtifactFinalization(t *testing.T) {
	t.Parallel()

	for _, tc := range registryInputWorkflows {
		workflow := loadWorkflow(t, tc.file)

		strategy := mappingValue(workflow.jobs["fetch-inputs"].node, "strategy")
		if scalarString(mappingValue(strategy, "max-parallel")) != "1" {
			t.Errorf("%s: registry input uploads must avoid concurrent Forgejo chunk finalization", tc.file)
		}

		matrix := mappingValue(strategy, "matrix")
		if scalarString(mappingValue(matrix, "build")) != "${{ fromJSON(needs.plan.outputs.builds) }}" {
			t.Errorf("%s: serialized fetch must still cover every planned image and architecture", tc.file)
		}

		buildStrategy := mappingValue(workflow.jobs["build"].node, "strategy")
		if mappingValue(buildStrategy, "max-parallel") != nil {
			t.Errorf("%s: registry input serialization must not serialize image builds", tc.file)
		}
	}
}

func TestPrivateRegistryFetchIsSeparatedFromConsumerBuild(t *testing.T) {
	t.Parallel()

	for _, tc := range registryInputWorkflows {
		workflow := loadWorkflow(t, tc.file)

		fetch, exists := workflow.jobs["fetch-inputs"]
		if !exists {
			t.Fatalf("%s: missing isolated registry fetch job", tc.file)
		}

		build := workflow.jobs["build"]
		if anyString(build.node, secretsWord.MatchString) {
			t.Fatalf("%s: consumer build must remain secret-free", tc.file)
		}

		fetchCommand := false

		for _, step := range fetch.steps() {
			run := scalarString(mappingValue(step, "run"))
			for _, forbidden := range []string{"platform checkout", "build-arch", "build-prepared", "base-images", "container build", "git -C source", "source/"} {
				if strings.Contains(run, forbidden) {
					t.Errorf("%s: credential fetch job executes consumer build/source: %s", tc.file, forbidden)
				}
			}

			if mappingValue(step, "uses") != nil {
				t.Errorf("%s: isolated fetch must not execute consumer actions", tc.file)
			}

			if strings.Contains(run, "release-images fetch-build-inputs") {
				fetchCommand = true

				if !strings.Contains(run, `unset REGISTRY_TOKEN`) || !strings.Contains(run, `unset registry_token`) || !strings.Contains(run, `trap 'rm -f -- "${auth_file}"' EXIT`) {
					t.Errorf("%s: fetch must isolate and clean its credential", tc.file)
				}
			}
		}

		if !fetchCommand {
			t.Errorf("%s: isolated fetch does not call the read-only CLI boundary", tc.file)
		}

		prepared := false
		restored := false

		for _, step := range build.steps() {
			run := scalarString(mappingValue(step, "run"))
			if strings.Contains(run, tc.download) {
				restored = true

				if tc.file == releaseImagesWorkflow && !strings.Contains(run, `--blob-store "${IMAGE_ARTIFACT_NAMESPACE}-input-blob"`) {
					t.Error("prepared input downloads must restore their upload blob store")
				}
			}

			if strings.Contains(run, tc.build) {
				prepared = true

				if tc.file == releaseImagesWorkflow && !strings.Contains(run, "container builder-identity") {
					t.Error("prepared builds must preserve native builder identity checks")
				}

				if tc.file == baseImagesWorkflow && !strings.Contains(run, "--inputs-dir registry-inputs") {
					t.Error("a child base must build from the fetched parent layout")
				}

				// A stage of siblings is a build job for each: the job builds
				// its matrix image alone, from that image's own fetched parent.
				if tc.file == baseImagesWorkflow && !strings.Contains(run, `--image "${IMAGE_NAME:?}"`) {
					t.Error("a base build job must build only its own image of the stage")
				}
			}
		}

		if !prepared {
			t.Errorf("%s: consumer build does not consume isolated inputs", tc.file)
		}

		if !restored {
			t.Errorf("%s: consumer build does not download prepared registry inputs", tc.file)
		}
	}
}

// A base stage holds the registry token in two jobs alone, the parent fetch
// and the publication, and neither a failed plan nor a failed build reaches
// the next job: a root skips its fetch, so both conditions are written out.
func TestBaseImagesStageKeepsItsTokenAndItsGates(t *testing.T) {
	t.Parallel()

	workflow := loadWorkflow(t, baseImagesWorkflow)

	for name, job := range workflow.jobs {
		holds := anyString(job.node, secretsWord.MatchString)
		if may := name == "fetch-inputs" || name == "publish"; holds != may {
			t.Errorf("job %s: reads a secret = %t, want %t", name, holds, may)
		}
	}

	// The plan learns the builder, so a kaniko child stops before its fetch.
	if got := scalarString(mappingValue(mappingValue(workflow.jobs["plan"].node, "env"), "CI_IMAGE_BUILDER")); got != "${{ needs.coordinates.outputs.builder }}" {
		t.Errorf("job plan admits the stage for builder %q, want the validated one", got)
	}

	for job, want := range map[string]string{
		"fetch-inputs": "${{ needs.coordinates.result == 'success' && needs.plan.result == 'success' && inputs['parent-base-json'] != '' }}",
		"build":        "${{ always() && needs.coordinates.result == 'success' && needs.plan.result == 'success' && (needs.fetch-inputs.result == 'success' || (needs.fetch-inputs.result == 'skipped' && inputs['parent-base-json'] == '')) }}",
		"publish":      "${{ always() && needs.coordinates.result == 'success' && needs.plan.result == 'success' && needs.build.result == 'success' }}",
	} {
		if got := scalarString(mappingValue(workflow.jobs[job].node, "if")); got != want {
			t.Errorf("job %s runs if %q, want %q", job, got, want)
		}
	}
}

// A child is planned from a parent projection of the verification workflow,
// for a base promoted in the run and for one reused alike. Both outputs come
// from the one step that verified the evidence, and no shell of the workflow
// composes the map of reused parents: the engine writes it, and only for
// authenticated reads.
func TestBaseParentProjectionsComeFromTheVerifyingStep(t *testing.T) {
	t.Parallel()

	const file = ".forgejo/workflows/verify-base-container-images.yml"

	called := mappingValue(mappingValue(mappingValue(loadDocument(t, file), "on"), "workflow_call"), "outputs")
	verify := loadWorkflow(t, file).jobs["verify-base-container-images"]

	for output, name := range map[string]string{"parent-base-json": "parent_base_json", "parent-bases-json": "parent_bases_json"} {
		if got, want := scalarString(mappingValue(mappingValue(called, output), "value")), "${{ jobs.verify-base-container-images.outputs."+name+" }}"; got != want {
			t.Errorf("%s: output %s is %q, want %q", file, output, got, want)
		}

		if got, want := scalarString(mappingValue(mappingValue(verify.node, "outputs"), name)), "${{ steps.verify.outputs."+name+" }}"; got != want {
			t.Errorf("%s: job output %s is %q, want %q", file, name, got, want)
		}
	}

	for _, step := range verify.steps() {
		run := scalarString(mappingValue(step, "run"))
		if strings.Contains(run, "parent_bases_json") {
			t.Errorf("%s: a step writes parent_bases_json itself", file)
		}

		if strings.Contains(run, "parent_base_json") != (scalarString(mappingValue(step, "id")) == "verify") {
			t.Errorf("%s: parent_base_json is written outside the verify step, or not by it", file)
		}
	}
}
