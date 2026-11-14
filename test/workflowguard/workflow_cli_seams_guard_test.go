// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPublishContainerValidatesNamespaceForSelectedRegistry(t *testing.T) {
	t.Parallel()

	env := workflowStepEnv(t, "publish-container.yml", "prep", "Validate Image Namespace")
	require.Equal(t, "${{ inputs.registry }}", env["CONTAINER_REGISTRY"])
	require.Equal(t, "${{ inputs.registry }}", env["ENFORCE_NAMESPACE_ON"])
}

// The Maven and Gradle builds upload target/ and build/libs/ below the
// artifact's working directory. The image build restores each download
// there and verifies the jars where a Containerfile copies them from.
func TestPublishContainerRestoresJVMArtifactsWhereTheyWereBuilt(t *testing.T) {
	t.Parallel()

	publish, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", "publish-container.yml"))
	require.NoError(t, err)
	stage, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", "release-publish-stage.yml"))
	require.NoError(t, err)

	for _, build := range []struct{ name, variable, output string }{
		{"maven", "MAVEN_WORKING_DIRECTORY", "target"},
		{"gradle", "GRADLE_WORKING_DIRECTORY", "build/libs"},
	} {
		require.Contains(t, string(publish), `run: forge-ci artifact download --name "$DOWNLOAD_NAME" --dir "./$`+build.variable+`"`+"\n")
		require.Contains(t, string(publish), "run: forge-ci container validate artifacts --project-type "+build.name+` --artifact-dir "./$`+build.variable+"/"+build.output+`"`+"\n")
		require.Contains(t, string(stage), build.name+"-working-directory: ${{ matrix.container."+build.name+"_working_directory || '.' }}\n")
	}

	// A direct caller chooses both directories: the job refuses one that
	// leaves the source checkout before anything is unpacked there.
	require.Contains(t, string(publish), `--context "$BUILD_CONTEXT" --source-dir "$MAVEN_WORKING_DIRECTORY" --source-dir "$GRADLE_WORKING_DIRECTORY"`+"\n")

	// The Gradle layout rests on these two upload patterns: their common
	// root is the working directory, so the artifact keeps build/libs.
	gradle, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", "build-gradle-app.yml"))
	require.NoError(t, err)
	require.Contains(t, string(gradle), "${{ inputs['working-directory'] }}/build/libs/*.jar\n")
	require.Contains(t, string(gradle), "${{ inputs['working-directory'] }}/**/build/libs/*.jar\n")
}

func workflowStepEnv(t *testing.T, workflowName, jobName, stepName string) map[string]string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", workflowName))
	require.NoError(t, err)

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(body, &workflow))

	job, ok := workflow.Jobs[jobName]
	require.Truef(t, ok, "%s has no job %q", workflowName, jobName)

	for _, step := range job.Steps {
		if step.Name == stepName {
			return step.Env
		}
	}

	require.FailNowf(t, "workflow step not found", "%s job %q has no step %q", workflowName, jobName, stepName)

	return nil
}
