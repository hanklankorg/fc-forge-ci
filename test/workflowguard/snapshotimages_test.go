// SPDX-FileCopyrightText: The Forge CI Authors
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
	var needs []string
	for _, dependency := range mappingChild(publish, "needs").Content {
		needs = append(needs, dependency.Value)
	}
	require.Contains(t, needs, "build", "all image/architecture legs must finish before ANY publisher")
	require.NotContains(t, scalarChild(publish, "if"), "always()")
	require.Equal(t, "false", scalarChild(mappingChild(build, "strategy"), "fail-fast"))
	require.Equal(t, "false", scalarChild(mappingChild(mappingChild(jobs, "build-artifacts"), "strategy"), "fail-fast"))
	require.Contains(t, scalarChild(build, "runs-on"), "matrix.build.arch == 'arm64'")
	require.Contains(t, scalarChild(mappingChild(mappingChild(build, "strategy"), "matrix"), "build"), "needs.plan.outputs.builds")
	require.Contains(t, scalarChild(mappingChild(mappingChild(publish, "strategy"), "matrix"), "image"), "needs.plan.outputs.images")
	require.Contains(t, scalarChild(collect, "if"), "needs.publish.result == 'success'")
	for _, id := range []string{"plan", "build-artifacts", "prepare-context", "build"} {
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

func TestSnapshotImageWorkflowUsesVerifiedRunEngineAndFrozenSource(t *testing.T) {
	t.Parallel()
	doc, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/snapshot-images.yml"))
	require.NoError(t, err)
	jobs := mappingChild(doc, "jobs")
	for _, id := range []string{"build-artifacts", "prepare-context", "build", "publish", "collect"} {
		job := mappingChild(jobs, id)
		verified, used, downloaded, sourceCheckout := false, false, false, false
		for _, step := range mappingChild(job, "steps").Content {
			run, uses := scalarChild(step, "run"), scalarChild(step, "uses")
			if strings.HasPrefix(uses, "actions/download-artifact@") {
				require.Equal(t, "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c", uses)
				if strings.HasSuffix(scalarChild(mappingChild(step, "with"), "name"), "-inputs") {
					downloaded = true
				}
			}
			if strings.Contains(run, "sha256sum --check") {
				require.True(t, downloaded, "verify the engine downloaded from this plan run")
				require.Contains(t, scalarChild(mappingChild(step, "env"), "ENGINE_SHA256"), "needs.plan.outputs.engine-sha256")
				verified = true
			}
			if strings.HasPrefix(uses, "actions/checkout@") {
				with := mappingChild(step, "with")
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
				require.True(t, verified, "no engine command may precede run-engine verification in %s", id)
				used = true
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
		require.True(t, verified && used && downloaded, id)
		if id != "collect" {
			require.True(t, sourceCheckout, "%s must check out the frozen source before producer, context or policy work", id)
		}
	}
}

func TestSnapshotImageNativeEngineMatchesRunnerArchitecture(t *testing.T) {
	t.Parallel()
	doc, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/snapshot-images.yml"))
	require.NoError(t, err)
	jobs := mappingChild(doc, "jobs")
	plan := mappingChild(jobs, "plan")
	outputs := mappingChild(plan, "outputs")
	require.Equal(t, "${{ steps.source.outputs.sha }}", scalarChild(outputs, "source-sha"))
	require.Equal(t, "${{ needs.resolve-bootstrap.outputs.forge-ci-binary-ref }}", scalarChild(outputs, "engine-ref"))
	require.Equal(t, "${{ steps.forge-ci-bootstrap.outputs.commit }}", scalarChild(outputs, "bootstrap-sha"))
	retainedModule := false
	for _, step := range mappingChild(mappingChild(jobs, "plan"), "steps").Content {
		if strings.Contains(scalarChild(step, "run"), `cp .github-shared/go.mod "${RUNNER_TEMP}/snapshot-inputs/engine.go.mod"`) {
			retainedModule = true
		}
	}
	require.True(t, retainedModule)
	verifiedAMD64, nativeToolchain, installedARM64 := false, false, false
	for _, step := range mappingChild(mappingChild(jobs, "build"), "steps").Content {
		run := scalarChild(step, "run")
		if strings.Contains(run, "sha256sum --check") {
			require.Equal(t, "${{ matrix.build.arch == 'amd64' }}", scalarChild(step, "if"), "never put the plan runner's amd64 engine on an arm64 PATH")
			verifiedAMD64 = true
		}
		if strings.HasPrefix(scalarChild(step, "uses"), "actions/setup-go@") {
			require.Equal(t, "${{ matrix.build.arch == 'arm64' }}", scalarChild(step, "if"))
			require.Equal(t, "${{ runner.temp }}/snapshot-inputs/engine.go.mod", scalarChild(mappingChild(step, "with"), "go-version-file"))
			nativeToolchain = true
		}
		if strings.Contains(run, "bootstrap/install-forge-ci.sh") {
			require.True(t, nativeToolchain, "native source bootstrap needs the retained engine's Go version")
			require.Equal(t, "${{ matrix.build.arch == 'arm64' }}", scalarChild(step, "if"))
			env := mappingChild(step, "env")
			require.Equal(t, "${{ needs.plan.outputs.engine-ref }}", scalarChild(env, "FORGE_CI_BINARY_REF"))
			require.Equal(t, "${{ needs.plan.outputs.bootstrap-sha }}", scalarChild(env, "FORGE_CI_SOURCE_SHA"))
			require.Equal(t, "${{ runner.temp }}/snapshot-native-bin", scalarChild(env, "FORGE_CI_INSTALL_DIR"))
			require.Equal(t, `bash "${RUNNER_TEMP}/snapshot-inputs/bootstrap/install-forge-ci.sh" "$FORGE_CI_BINARY_REF"`, strings.TrimSpace(run))
			installedARM64 = true
		}
		if strings.Contains(run, "forge-ci ") {
			require.True(t, verifiedAMD64 && installedARM64, "every native path must install its admitted engine before commands run")
		}
	}
	require.True(t, verifiedAMD64 && installedARM64)
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
