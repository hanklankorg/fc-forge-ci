// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

func TestSnapshotImageWorkflowKeepsWholeMatrixPublicationBarrier(t *testing.T) {
	t.Parallel()
	doc, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/snapshot-images.yml"))
	require.NoError(t, err)

	jobs := mappingChild(doc, "jobs")
	require.Empty(t, snapshotBarrierViolations(jobs))
	build, publish, collect := mappingChild(jobs, "build"), mappingChild(jobs, "publish"), mappingChild(jobs, "collect")
	require.NotNil(t, build)
	require.NotNil(t, publish)
	require.NotNil(t, collect)

	needsNode := mappingChild(publish, "needs")

	needs := make([]string, 0, len(needsNode.Content))
	for _, dependency := range needsNode.Content {
		needs = append(needs, dependency.Value)
	}

	require.Contains(t, needs, "build", "all image/architecture legs must finish before ANY publisher")
	require.NotContains(t, scalarChild(publish, "if"), "always()")
	require.Equal(t, "false", scalarChild(mappingChild(build, "strategy"), "fail-fast"))
	require.Equal(t, "false", scalarChild(mappingChild(mappingChild(jobs, "build-artifacts"), "strategy"), "fail-fast"))
	require.Contains(t, scalarChild(build, "runs-on"), "(matrix.build.arch == 'arm64' && inputs['build-runs-on-linux-arm64']) || (matrix.build.arch == 'amd64' && inputs['build-runs-on-linux-amd64']) || ''")
	require.Contains(t, scalarChild(mappingChild(mappingChild(build, "strategy"), "matrix"), "build"), "needs.plan.outputs.builds")
	require.Contains(t, scalarChild(mappingChild(mappingChild(publish, "strategy"), "matrix"), "image"), "needs.plan.outputs.images")
	require.Contains(t, scalarChild(collect, "if"), "needs.publish.result == 'success'")

	for _, id := range []string{"plan", "build-artifacts", "build-files", "prepare-context", "build"} {
		job := mappingChild(jobs, id)
		for _, permission := range mappingChild(job, "permissions").Content {
			require.NotEqual(t, "write", permission.Value, "%s must have no write permission", id)
		}

		for _, step := range mappingChild(job, "steps").Content {
			run := scalarChild(step, "run")
			for _, forbidden := range []string{"snapshot-images publish", "container login", "--build-secret", "--auth-file", "secrets."} {
				require.NotContains(t, run, forbidden)
			}

			if env := mappingChild(step, "env"); env != nil {
				for _, entry := range env.Content {
					require.NotContains(t, entry.Value, "secrets.")
				}
			}
		}
	}

	require.Equal(t, "write", scalarChild(mappingChild(publish, "permissions"), "packages"))
}

// A file output is kept in the run alone, under a name that carries the
// snapshot version, for the documented seven days: no registry, no release.
func TestSnapshotFilesAreKeptAsARunArtifactOnly(t *testing.T) {
	t.Parallel()

	doc, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/snapshot-images.yml"))
	require.NoError(t, err)

	files := mappingChild(mappingChild(doc, "jobs"), "build-files")
	require.NotNil(t, files)
	require.Equal(t, "${{ fromJson(needs.plan.outputs.files) }}", scalarChild(mappingChild(mappingChild(files, "strategy"), "matrix"), "file"))

	uploads := 0

	for _, step := range mappingChild(files, "steps").Content {
		if !strings.HasPrefix(scalarChild(step, "uses"), "actions/upload-artifact@") {
			continue
		}

		uploads++

		with := mappingChild(step, "with")
		// Under the caller's namespace, where collect's receipt pattern cannot
		// match it and a second call in one run cannot collide.
		require.Equal(t, "${{ inputs['artifact-name'] }}-files-${{ matrix.file.artifact_name }}", scalarChild(with, "name"))
		require.Equal(t, "source/snapshot-files/", scalarChild(with, "path"))
		require.Equal(t, "7", scalarChild(with, "retention-days"))
	}

	require.Equal(t, 1, uploads)

	// The namespace is bounded where the plan is frozen, before any build.
	bounded := false

	for _, step := range mappingChild(mappingChild(mappingChild(doc, "jobs"), "plan"), "steps").Content {
		if scalarChild(step, "id") == "plan" {
			bounded = strings.Contains(scalarChild(step, "run"), `"$PUBLISH_FILES" == true && "${#ARTIFACT_NAMESPACE}" -gt 48`) &&
				scalarChild(mappingChild(step, "env"), "ARTIFACT_NAMESPACE") == "${{ inputs['artifact-name'] }}"
		}
	}

	require.True(t, bounded, "a file output's namespace must be bounded in the plan step")
}

// Every job runs the engine of the run and installs nothing: the container
// jobs the forge-ci baked into the admitted runtime image of their role, the
// host build legs the canonical CLI the caller's delivery built, received
// before the frozen source checkout.
func TestSnapshotImageWorkflowRunsTheAdmittedEngineAndFrozenSource(t *testing.T) {
	t.Parallel()

	body := reporoot.ReadFile(t, ".github/workflows/snapshot-images.yml")
	for _, legacy := range []string{"install-forge-ci.sh", "sha256sum --check", "install_trivy", "install_syft", "actions/setup-go@", "engine-sha256"} {
		require.NotContains(t, string(body), legacy)
	}

	doc, err := effectiveWorkflow(body)
	require.NoError(t, err)

	jobs := mappingChild(doc, "jobs")
	for id, image := range map[string]string{
		"plan":            "${{ inputs['runtime-image'] }}",
		"prepare-context": "${{ inputs['runtime-image'] }}",
		"collect":         "${{ inputs['runtime-image'] }}",
		"publish":         "${{ inputs['runtime-image-full'] }}",
	} {
		require.Equal(t, image, scalarChild(mappingChild(mappingChild(jobs, id), "container"), "image"), id)
	}

	require.Contains(t, scalarChild(mappingChild(mappingChild(jobs, "build-artifacts"), "container"), "image"), "inputs['runtime-image-go']")
	require.Nil(t, mappingChild(mappingChild(jobs, "build"), "container"), "Buildah needs the host")

	for _, id := range []string{"build-artifacts", "prepare-context", "build", "publish", "collect"} {
		job := mappingChild(jobs, id)
		sourceCheckout, received := false, id != "build"

		for _, step := range mappingChild(job, "steps").Content {
			run, uses := scalarChild(step, "run"), scalarChild(step, "uses")
			if uses == "./_forge_ci_bootstrap/.github/actions/consume-canonical-cli" {
				require.Equal(t, "host", scalarChild(mappingChild(step, "with"), "tool-profile"))
				require.False(t, sourceCheckout, "the canonical CLI is received before the frozen source checkout")

				received = true
			}

			if strings.HasPrefix(uses, "actions/checkout@") {
				with := mappingChild(step, "with")
				if scalarChild(with, "path") == "_forge_ci_bootstrap" {
					continue
				}

				require.Equal(t, "${{ needs.plan.outputs.source-sha }}", scalarChild(with, "ref"))
				require.Equal(t, "false", scalarChild(with, "persist-credentials"))

				if id == "publish" {
					require.Equal(t, "scan-policy", scalarChild(with, "path"))
					require.Equal(t, ".trivyignore.yaml", scalarChild(with, "sparse-checkout"))
					require.Equal(t, "false", scalarChild(with, "sparse-checkout-cone-mode"))
					require.Nil(t, mappingChild(step, "env"), "policy checkout does not receive publication credentials")
				}

				sourceCheckout = true
			}

			if strings.Contains(run, "forge-ci ") {
				require.True(t, received, "no engine command may precede the canonical receive in %s", id)
			}

			if strings.Contains(run, "snapshot-images build ") {
				require.True(t, sourceCheckout)
				require.Contains(t, run, "--dependencies-file")
				require.Contains(t, run, "env -u GH_TOKEN -u GITHUB_TOKEN")
			}

			if strings.Contains(run, "snapshot-images build-artifact ") {
				require.True(t, sourceCheckout)
				require.Contains(t, run, "env -u GH_TOKEN -u GITHUB_TOKEN")
				require.Contains(t, run, `--plan "${RUNNER_TEMP}/snapshot-inputs/snapshot-plan.json"`)
			}

			if strings.Contains(run, "snapshot-images publish") {
				require.True(t, sourceCheckout)
				require.Equal(t, "scan-policy", scalarChild(step, "working-directory"))
			}
		}

		require.True(t, received, id)

		if id != "collect" {
			require.True(t, sourceCheckout, "%s must check out the frozen source before producer, context or policy work", id)
		}
	}
}

// Both native legs receive the file of their own architecture from the one
// canonical delivery, so no leg installs from source and the plan retains
// only the plan: an arch-conditional engine step would be the old split.
func TestSnapshotImageNativeLegsShareOneDelivery(t *testing.T) {
	t.Parallel()
	doc, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/snapshot-images.yml"))
	require.NoError(t, err)

	jobs := mappingChild(doc, "jobs")
	outputs := mappingChild(mappingChild(jobs, "plan"), "outputs")
	require.Equal(t, "${{ steps.source.outputs.sha }}", scalarChild(outputs, "source-sha"))
	require.Nil(t, mappingChild(outputs, "engine-ref"))
	require.Nil(t, mappingChild(outputs, "bootstrap-sha"))

	for _, step := range mappingChild(mappingChild(jobs, "plan"), "steps").Content {
		require.NotContains(t, scalarChild(step, "run"), "command -v forge-ci", "the plan does not retain an engine copy")
	}

	build := mappingChild(jobs, "build")
	require.Contains(t, scalarChild(build, "runs-on"), "(matrix.build.arch == 'arm64' && inputs['build-runs-on-linux-arm64']) || (matrix.build.arch == 'amd64' && inputs['build-runs-on-linux-amd64']) || ''")

	for _, step := range mappingChild(build, "steps").Content {
		require.NotContains(t, scalarChild(step, "if"), "matrix.build.arch", "no arch-conditional engine step: %s", scalarChild(step, "name"))
	}
}

func TestSnapshotRootPreservesLogicalBranchAndSameSourceAcrossOutputs(t *testing.T) {
	t.Parallel()
	doc, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/release-snapshot-orchestrator.yml"))
	require.NoError(t, err)

	inputs := mappingChild(mappingChild(mappingChild(doc, "on"), "workflow_call"), "inputs")
	require.Equal(t, "true", scalarChild(mappingChild(inputs, "publish-containers"), "default"))
	jobs := mappingChild(doc, "jobs")
	images := mappingChild(jobs, "execute-snapshot-images")
	with := mappingChild(images, "with")

	const plan = "needs.setup-snapshot.outputs.snapshot-release-plan-json"
	require.Equal(t, "${{ fromJson("+plan+").context.branch }}", scalarChild(with, "branch"))
	require.Equal(t, "${{ fromJson("+plan+").context.release_sha }}", scalarChild(with, "source-sha"))
	require.Equal(t, "${{ inputs['publish-containers'] }}", scalarChild(with, "publish-containers"))
	require.Equal(t, "true", scalarChild(with, "use-build-artifacts"), "root reuses its already completed artifact build stage")
	require.Contains(t, needsOf(images), "execute-snapshot-build-stage")

	for _, id := range []string{"execute-snapshot-build-stage", "execute-snapshot-publish-stage"} {
		require.Equal(t, "${{ "+plan+" }}", scalarChild(mappingChild(mappingChild(jobs, id), "with"), "snapshot-release-plan-json"))
	}

	for _, file := range []string{"release-snapshot-build-stage.yml", "release-snapshot-publish-stage.yml"} {
		stage, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/"+file))
		require.NoError(t, err)

		stageJobs := mappingChild(stage, "jobs")
		checked := 0

		for i := 1; i < len(stageJobs.Content); i += 2 {
			if branch := scalarChild(mappingChild(stageJobs.Content[i], "with"), "branch"); branch != "" {
				require.Equal(t, "${{ fromJson(inputs['snapshot-release-plan-json']).context.release_sha }}", branch, file)

				checked++
			}
		}

		require.Positive(t, checked)

		if file == "release-snapshot-publish-stage.yml" {
			npm := mappingChild(stageJobs, "npm")
			require.Equal(t, "${{ fromJson(inputs['snapshot-release-plan-json']).context.branch }}", scalarChild(mappingChild(npm, "with"), "source-branch"), "package version metadata retains the logical branch while checkout uses the SHA")
		}
	}

	summary := mappingChild(jobs, "snapshot-release-summary")

	var needsImages bool
	for _, need := range mappingChild(summary, "needs").Content {
		needsImages = needsImages || need.Value == "execute-snapshot-images"
	}

	require.True(t, needsImages)

	for _, step := range mappingChild(summary, "steps").Content {
		if scalarChild(step, "run") == "forge-ci report snapshot-release" {
			env := mappingChild(step, "env")
			require.Equal(t, "${{ needs.execute-snapshot-images.result }}", scalarChild(env, "SNAPSHOT_IMAGE_RESULT"))
			require.Equal(t, "${{ needs.execute-snapshot-images.outputs.container-images-json }}", scalarChild(env, "CONTAINER_IMAGES_JSON"))
		}
	}

	imageDoc, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/snapshot-images.yml"))
	require.NoError(t, err)

	imageInputs := mappingChild(mappingChild(mappingChild(imageDoc, "on"), "workflow_call"), "inputs")
	require.Equal(t, "true", scalarChild(mappingChild(imageInputs, "publish-containers"), "default"))
	require.Equal(t, "false", scalarChild(mappingChild(imageInputs, "use-build-artifacts"), "default"), "standalone workflow builds its declared dependency producers automatically")

	for _, step := range mappingChild(mappingChild(mappingChild(imageDoc, "jobs"), "plan"), "steps").Content {
		if strings.Contains(scalarChild(step, "run"), "snapshot-images plan") {
			require.Equal(t, "${{ inputs['publish-containers'] }}", scalarChild(mappingChild(step, "env"), "PUBLISH_CONTAINERS"))
			require.Contains(t, scalarChild(step, "run"), `--publish="$PUBLISH_CONTAINERS"`)
		}
	}
}
