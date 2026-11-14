// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type bootstrapWorkflow struct {
	Jobs map[string]struct {
		With  map[string]string `yaml:"with"`
		Steps []struct {
			ID   string            `yaml:"id"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readBootstrapWorkflow(t *testing.T, path string) bootstrapWorkflow {
	t.Helper()

	body, err := os.ReadFile(path)
	require.NoError(t, err)

	var workflow bootstrapWorkflow
	require.NoError(t, yaml.Unmarshal(body, &workflow))

	return workflow
}

// resolvedForgeCIRepository is the repository the resolver admitted. Jobs take
// it from there rather than re-reading job.workflow_repository, which is empty
// on GitHub Enterprise Server.
const resolvedForgeCIRepository = "${{ needs.resolve-bootstrap.outputs.forge-ci-repository }}"

func TestBootstrapResolverRefusesAnUnknownRepository(t *testing.T) {
	t.Parallel()
	workflow := readBootstrapWorkflow(t, filepath.Join(reporoot.Path(t), ".github", "workflows", "resolve-forge-ci-bootstrap.yml"))

	steps := workflow.Jobs["resolve"].Steps
	require.NotEmpty(t, steps)
	require.Equal(t, "repository", steps[0].ID, "the repository is admitted before anything reads it")
	require.Equal(t, "${{ vars.FORGE_CI_REPOSITORY || job.workflow_repository }}", steps[0].Env["REPOSITORY"])

	for _, step := range steps[1:] {
		for _, value := range []string{step.With["repository"], step.Env["REPOSITORY"]} {
			if value != "" {
				require.Equal(t, "${{ steps.repository.outputs.repository }}", value)
			}
		}
	}

	for _, tc := range []struct{ repository, say string }{
		{"", "Set the FORGE_CI_REPOSITORY variable"},
		{"noslash", "must be owner/name"},
		{"owner/../name", "must be owner/name"},
		{"owner/name/extra", "must be owner/name"},
		{"examplescope/forge-ci", ""},
	} {
		t.Run(tc.repository, func(t *testing.T) {
			t.Parallel()

			output := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", steps[0].Run)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "REPOSITORY=" + tc.repository, "GITHUB_OUTPUT=" + output}

			out, err := cmd.CombinedOutput()
			if tc.say != "" {
				require.Error(t, err, "%s", out)
				require.Contains(t, string(out), tc.say)

				_, statErr := os.Stat(output)
				require.True(t, os.IsNotExist(statErr))

				return
			}

			require.NoError(t, err, "%s", out)
			body, err := os.ReadFile(output)
			require.NoError(t, err)
			require.Equal(t, "repository="+tc.repository+"\n", string(body))
		})
	}
}

func TestOnlyTheResolverReadsTheWorkflowRepository(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	for _, dir := range []string{filepath.Join(root, ".github", "workflows"), filepath.Join(root, ".github", "actions")} {
		require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".yml" {
				return err
			}

			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			want := 0
			if filepath.Base(path) == "resolve-forge-ci-bootstrap.yml" {
				want = 1
			}

			require.Equal(t, want, strings.Count(string(body), "job.workflow_repository"),
				"%s reads job.workflow_repository, which is empty on GitHub Enterprise Server; take needs.resolve-bootstrap.outputs.forge-ci-repository instead", path)

			return nil
		}))
	}
}

// TestBootstrapResolverAdmitsOnlyACommitAndDigests runs the resolver's
// admission: the engine is named by its full commit, never a tag, a branch or
// the latest release, and every image line is a digest, or empty where the
// caller marked it as run only when its plan asks for it.
func TestBootstrapResolverAdmitsOnlyACommitAndDigests(t *testing.T) {
	t.Parallel()
	workflow := readBootstrapWorkflow(t, filepath.Join(reporoot.Path(t), ".github", "workflows", "resolve-forge-ci-bootstrap.yml"))
	script := bootstrapSelectionScript(t, workflow, "admit")

	commit := strings.Repeat("a", 40)
	base := "ghcr.io/diggsweden/forge-ci-runtime-base@sha256:" + strings.Repeat("b", 64)

	for _, tc := range []struct {
		name, commit, images, say string
	}{
		{"commit and digest", commit, "runtime-image=" + base + "\n", ""},
		{"no images", commit, "", ""},
		{"optional family left out", commit, "runtime-image=" + base + "\nruntime-image-go?=\n", ""},
		{"optional family by digest", commit, "runtime-image-go?=" + base + "\n", ""},
		{"release tag", "v0.90.0", "", "full engine commit"},
		{"branch", "main", "", "full engine commit"},
		{"empty commit", "", "", "full engine commit"},
		{"sha-256 object id", strings.Repeat("a", 64), "", "full engine commit"},
		{"tagged image", commit, "runtime-image=ghcr.io/diggsweden/forge-ci-runtime-base:v0.90.0\n", "runtime-image must be a runtime image pinned by digest"},
		{"empty required image", commit, "runtime-image-full=\n", "runtime-image-full must be a runtime image pinned by digest"},
		{"tagged optional image", commit, "runtime-image-java?=ghcr.io/x/forge-ci-runtime-java-25:sha-abcdef0\n", "runtime-image-java must be"},
		{"not a line", commit, "ghcr.io/x@sha256:" + strings.Repeat("c", 64) + "\n", "is not input=reference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", script)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "ENGINE_COMMIT=" + tc.commit, "IMAGES=" + tc.images}

			out, err := cmd.CombinedOutput()
			if tc.say == "" {
				require.NoError(t, err, "%s", out)

				return
			}

			require.Error(t, err, "%s", out)
			require.Contains(t, string(out), tc.say)
		})
	}
}

func TestPRQualityForwardsTheSelectedBinaryToEveryLinter(t *testing.T) {
	t.Parallel()

	workflow := readBootstrapWorkflow(t, filepath.Join(reporoot.Path(t), ".github", "workflows", "pullrequest-quality-stage.yml"))
	for _, job := range []string{"nanolinter", "megalinter", "swift"} {
		require.Equal(t, engineCommitInput, workflow.Jobs[job].With["forge-ci-binary-ref"], job)
	}
}

func bootstrapSelectionScript(t *testing.T, workflow bootstrapWorkflow, id string) string {
	t.Helper()

	for _, step := range workflow.Jobs["resolve"].Steps {
		if step.ID == id {
			return step.Run
		}
	}

	t.Fatalf("missing bootstrap selection step %s", id)

	return ""
}

// engineCommitInput is how a workflow names its engine: the caller's full
// commit, admitted by the resolver, never an output relayed from a job.
const engineCommitInput = "${{ inputs['forge-ci-binary-ref'] }}"

type reusableWorkflow struct {
	On struct {
		WorkflowCall struct {
			Inputs map[string]struct {
				Default any `yaml:"default"`
			} `yaml:"inputs"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with"`
	} `yaml:"jobs"`
}

func readReusableWorkflows(t *testing.T) map[string]reusableWorkflow {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(reporoot.Path(t), ".github", "workflows", "*.yml"))
	require.NoError(t, err)

	workflows := map[string]reusableWorkflow{}

	for _, path := range paths {
		body, err := os.ReadFile(path)
		require.NoError(t, err)

		var workflow reusableWorkflow
		require.NoError(t, yaml.Unmarshal(body, &workflow), path)

		workflows[filepath.Base(path)] = workflow
	}

	return workflows
}

// TestRuntimeImageDefaultsResolveTheServingRepository keeps every runtime
// image input free of a default. A literal default names one registry and one
// release, so a fork serving these workflows, or a caller on a later release,
// ran upstream's older image without asking for it; the caller passes the
// digest of the release it calls instead.
func TestRuntimeImageDefaultsResolveTheServingRepository(t *testing.T) {
	t.Parallel()

	for name, workflow := range readReusableWorkflows(t) {
		for input, spec := range workflow.On.WorkflowCall.Inputs {
			if strings.HasPrefix(input, "runtime-image") {
				require.Emptyf(t, spec.Default, "%s: input %s defaults to a fixed image; leave it without a default and pass the digest", name, input)
			}
		}
	}
}

// TestLocalCallsForwardTheEngineCommit makes every workflow hand its own
// engine commit to each local reusable workflow that takes one, so one run
// names one engine revision. The value is the caller's input, which the
// resolver admits as a full commit; nothing relays or re-resolves it.
func TestLocalCallsForwardTheEngineCommit(t *testing.T) {
	t.Parallel()

	workflows := readReusableWorkflows(t)
	calls := 0

	for name, workflow := range workflows {
		if _, own := workflow.On.WorkflowCall.Inputs[binaryRefInput]; !own {
			continue // An entry workflow (Self Pull Request, Self Release) names its commit from its event.
		}

		for jobName, job := range workflow.Jobs {
			callee, local := strings.CutPrefix(job.Uses, "./.github/workflows/")
			if !local {
				continue
			}

			if _, takes := workflows[callee].On.WorkflowCall.Inputs[binaryRefInput]; !takes {
				continue
			}

			calls++

			require.Equalf(t, engineCommitInput, job.With[binaryRefInput],
				"%s/%s calls %s without its own %s", name, jobName, callee, binaryRefInput)
		}
	}

	require.Positive(t, calls)
}
