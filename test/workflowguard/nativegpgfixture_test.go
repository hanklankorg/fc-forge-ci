// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestNativeGPGFixtureUsesRealGoAndOrdinaryReleaseOwner(t *testing.T) {
	root := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-go-cli/consumer")
	read := func(path string) map[string]any {
		body, err := os.ReadFile(filepath.Join(root, path))
		require.NoError(t, err)

		var document map[string]any
		require.NoError(t, yaml.Unmarshal(body, &document))

		return document
	}
	config := read(".forge-ci/artifacts-gpg.yml")
	require.Equal(t, "gpg", config["sign"].(map[string]any)["method"])
	require.Equal(t, "gpg", config["git-signing"].(map[string]any)["method"])
	require.Empty(t, config["containers"])
	artifacts := config["artifacts"].([]any)
	require.Len(t, artifacts, 1)
	artifact := artifacts[0].(map[string]any)
	require.Equal(t, "go", artifact["project-type"])
	require.Equal(t, ".", artifact["working-directory"])
	build := artifact["config"].(map[string]any)
	require.Equal(t, "artifact-first", build["build-mode"])
	require.Equal(t, "./cmd/my-cli", build["main-package"])
	require.Equal(t, "linux/amd64", build["platforms"])
	require.Equal(t, false, build["skip-tests"])

	_, err := os.Stat(filepath.Join(root, "cmd/my-cli/main.go"))
	require.NoError(t, err)

	workflow := read("test/live/github-gpg/workflows/release-gpg.yml")

	require.NoFileExists(t, filepath.Join(root, "test/live/github/workflows/release-gpg.yml"))
	profileWorkflows, err := filepath.Glob(filepath.Join(root, "test/live/github-gpg/workflows/*.yml"))
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "test/live/github-gpg/workflows/release-gpg.yml")}, profileWorkflows)

	jobs := workflow["jobs"].(map[string]any)
	require.Len(t, jobs, 1)
	job := jobs["release"].(map[string]any)
	require.Equal(t, "diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@PIN_FORGE_CI_SHA", job["uses"])
	require.NotContains(t, job, "steps")
	inputs := job["with"].(map[string]any)
	require.Equal(t, ".forge-ci/artifacts-gpg.yml", inputs["artifacts-config"])
	require.Equal(t, true, inputs["release.signartifacts"])
	require.Equal(t, "release-artifacts/*/my-cli-*", inputs["release.attachartifacts"])
	require.Equal(t, "github-cli", inputs["release-publisher"])

	secrets := job["secrets"].(map[string]any)
	require.Len(t, secrets, 4)

	for _, name := range []string{"RELEASE_TOKEN", "RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_PASSPHRASE", "RELEASE_GPG_PUBLIC_KEY"} {
		require.Equal(t, "${{ secrets."+name+" }}", secrets[name])
	}

	// The normal bundle profile remains explicitly Sigstore; a GPG release
	// cannot be promoted to a mixed-signature or offered-bundle acceptance case.
	original := read(".forge-ci/artifacts.yml")
	require.Equal(t, "sigstore", original["sign"].(map[string]any)["method"])

	originalWorkflow := read("test/live/github/workflows/release.yml")
	originalJob := originalWorkflow["jobs"].(map[string]any)["release"].(map[string]any)
	require.Equal(t, ".forge-ci/artifacts.yml", originalJob["with"].(map[string]any)["artifacts-config"])
}
