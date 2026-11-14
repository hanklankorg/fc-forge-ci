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

// A Containerfile that rebuilds an artifact forge-ci built, and one that names
// an image without its registry, are each warned about by one engine command.
// Each forge runs both where the checkout is: in the lint job of a pull
// request and in the plan of a release.
func TestContainerfileWarningsRunOnEveryForge(t *testing.T) {
	t.Parallel()

	for file, call := range map[string]string{
		".github/workflows/lint-nanolinter.yml": "working-directory: src\n        run: |\n" +
			"          forge-ci container validate rebuilds\n          forge-ci container validate image-names\n",
		".github/workflows/release-orchestrator.yml": "run: |\n          forge-ci container validate rebuilds\n" +
			"          forge-ci container validate image-names\n        env:\n          ARTIFACTS_CONFIG: ${{ inputs['artifacts-config'] }}\n",
		".forgejo/actions/lint-nanolinter/action.yml": "run_forge_ci container validate rebuilds\n        run_forge_ci container validate image-names\n",
		".forgejo/workflows/prepare-release.yml": `run_forge_ci container validate rebuilds "${file[@]}"` + "\n" +
			`          run_forge_ci container validate image-names "${file[@]}"` + "\n",
		"templates/nanolinter.yml": "run_forge_ci container validate rebuilds\n          run_forge_ci container validate image-names\n",
		"templates/release.yml": `(cd -- "${work}/consumer" && "${engine}" container validate rebuilds "${declared[@]}")` + "\n" +
			`      (cd -- "${work}/consumer" && "${engine}" container validate image-names "${declared[@]}")` + "\n",
	} {
		body, err := os.ReadFile(filepath.Join(reporoot.Path(t), filepath.FromSlash(file)))
		require.NoError(t, err)
		require.Contains(t, string(body), call, file)
	}
}

// The engine adds --baseline-ref origin/<target branch> to nanolinter verify
// on a pull request. The lint job's checkout must hold that ref, and the
// forge file must leave the flag to the engine.
func TestLintCheckoutHoldsThePullRequestBaseline(t *testing.T) {
	t.Parallel()

	read := func(file string) string {
		body, err := os.ReadFile(filepath.Join(reporoot.Path(t), filepath.FromSlash(file)))
		require.NoError(t, err)

		return string(body)
	}

	github := read(".github/workflows/lint-nanolinter.yml")
	require.Equal(t, "true", workflowStepEnv(t, "lint-nanolinter.yml", "nanolinter-lint", "Checkout repository")["CHECKOUT_FETCH_ALL_REFS"])
	require.Contains(t, github, `"$NANOLINTER_IMAGE" verify "$@" --report sarif --out - || rc=$?`+"\n")

	gitlab := read("templates/nanolinter.yml")
	require.Contains(t, gitlab, `"+refs/heads/${target}:refs/remotes/origin/${target}"`)
	require.Contains(t, gitlab, `GIT_DEPTH: "0"`)

	for file, body := range map[string]string{
		".github/workflows/lint-nanolinter.yml":       github,
		"templates/nanolinter.yml":                    gitlab,
		".forgejo/actions/lint-nanolinter/action.yml": read(".forgejo/actions/lint-nanolinter/action.yml"),
	} {
		require.NotContains(t, body, "--baseline-ref \"", file)
		require.NotContains(t, body, "--baseline-ref $", file)
	}
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
