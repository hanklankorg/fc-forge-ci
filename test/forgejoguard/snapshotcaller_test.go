// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLISnapshotBuildsWithoutPublicationOrSecrets(t *testing.T) {
	t.Parallel()

	workflow := loadWorkflow(t, "examples/go-cli/forgejo/snapshot.yml")
	require.Len(t, workflow.jobs, 1)
	build := workflow.jobs["build"]
	require.False(t, anyString(build.node, secretReference.MatchString))

	actions := make([]string, 0, len(build.steps()))

	for _, step := range build.steps() {
		uses := scalarString(mappingValue(step, "uses"))
		actions = append(actions, uses)

		require.Empty(t, scalarString(mappingValue(step, "run")), "snapshot must delegate shared behavior")

		if strings.Contains(uses, "/build-go@") {
			inputs := mappingValue(step, "with")
			require.Equal(t, "false", scalarString(mappingValue(inputs, "enable-build-sbom")))
			require.Nil(t, mappingValue(inputs, "enforce-application-tests"), "consumer must inherit administrator enforcement, not set policy itself")
			require.Equal(t, "${{ forgejo.sha }}", scalarString(mappingValue(inputs, "commit")))
		}
	}

	require.Equal(t, []string{
		"https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/checkout-consumer@PIN_FORGE_CI_SHA",
		"https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/build-go@PIN_FORGE_CI_SHA",
	}, actions)
}

// The consumer-side snapshot callers: the Go service's, container-only, and
// the Maven application's, artifact-first, whose producer builds in the
// digest-pinned Java toolchain image the caller names.
func TestSnapshotCallerDelegatesWithoutReleaseAuthority(t *testing.T) {
	t.Parallel()

	for path, producerImage := range map[string]string{
		"examples/go-service/forgejo/snapshot.yml": "artifact-job-image-go",
		"examples/maven-app/forgejo/snapshot.yml":  "artifact-job-image-java",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			workflow := loadWorkflow(t, path)
			require.Len(t, workflow.jobs, 1)
			caller := workflow.jobs["snapshot"]
			require.Equal(t, "OWNER/forge-ci/.forgejo/workflows/snapshot-images.yml@PIN_FORGE_CI_SHA", scalarString(mappingValue(caller.node, "uses")))
			require.Nil(t, mappingValue(caller.node, "runs-on"), "same-instance workflow expansion chooses the native runners")
			require.Empty(t, caller.steps(), "the caller must not copy snapshot operations")
			require.Equal(t, "forgejo.ref == 'refs/heads/main'", scalarString(mappingValue(caller.node, "if")))
			inputs := mappingValue(caller.node, "with")
			require.Equal(t, "PIN_FORGE_CI_SHA", scalarString(mappingValue(inputs, "forge-ci-sha")))
			require.Equal(t, "main", scalarString(mappingValue(inputs, "branch")))
			require.Contains(t, scalarString(mappingValue(inputs, producerImage)), "@sha256:", "the producer toolchain image is pinned by digest")
			secrets := mappingEntries(mappingValue(caller.node, "secrets"))
			require.Len(t, secrets, 1)
			require.Equal(t, "SNAPSHOT_REGISTRY_PASSWORD", scalarString(secrets[0].Key))
			require.Equal(t, "${{ secrets.SNAPSHOT_REGISTRY_PASSWORD }}", scalarString(secrets[0].Value))

			for _, forbidden := range []string{"RELEASE_COSIGN", "RELEASE_GPG", "RELEASE_SSH", "RELEASE_TOKEN", "signer-image"} {
				require.NotContains(t, caller.node.String(), forbidden)
			}
		})
	}
}

// The files-only caller asks for snapshot files and for nothing a release
// or a registry needs: no secret, no image repository, no registry account.
func TestSnapshotFilesCallerHoldsNoCredential(t *testing.T) {
	t.Parallel()

	workflow := loadWorkflow(t, "examples/go-cli/forgejo/snapshot-files.yml")
	require.Len(t, workflow.jobs, 1)

	caller := workflow.jobs["snapshot"]
	require.Equal(t, "OWNER/forge-ci/.forgejo/workflows/snapshot-images.yml@PIN_FORGE_CI_SHA", scalarString(mappingValue(caller.node, "uses")))
	require.Empty(t, caller.steps(), "the caller must not copy snapshot operations")
	require.Nil(t, mappingValue(caller.node, "secrets"))

	inputs := mappingValue(caller.node, "with")
	require.Equal(t, "true", scalarString(mappingValue(inputs, "publish-files")))
	require.Equal(t, "PIN_FORGE_CI_SHA", scalarString(mappingValue(inputs, "forge-ci-sha")))
	require.Contains(t, scalarString(mappingValue(inputs, "artifact-job-image-go")), "@sha256:", "the toolchain image is pinned by digest")
	require.Nil(t, mappingValue(inputs, "image-repository"))
	require.Nil(t, mappingValue(inputs, "registry-user"))
}
