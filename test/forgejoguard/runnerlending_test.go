// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const (
	lenderExample = "examples/runner-lender/github-actions/lend-runner.yml"
	lendScript    = "scripts/runner-lender/lend-forgejo-runner.sh"

	// containerRunnerVariable is what a job that runs Buildah or Podman selects
	// its runner by. A pool that shares its kernel denies the user namespaces
	// they need, so these jobs never default to CI_RUNNER_LABEL.
	containerRunnerVariable = "CI_RUNNER_LABEL_CONTAINER"
	requestRunnerVerb       = "platform request-runner"
)

// containerBuildingActions are the layer's actions that run Buildah or Podman.
var containerBuildingActions = regexp.MustCompile(`/\.forgejo/actions/(setup-buildah|setup-image-builder)@`)

// forgejoExampleWorkflows lists the signer example and every ecosystem example's
// Forgejo workflows.
func forgejoExampleWorkflows(t *testing.T) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(reporoot.Path(t), "examples", "*", "forgejo", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}

	workflows := make([]string, 0, 1+len(matches))
	workflows = append(workflows, signerImageExample)

	for _, match := range matches {
		relative, relErr := filepath.Rel(reporoot.Path(t), match)
		if relErr != nil {
			t.Fatal(relErr)
		}

		workflows = append(workflows, filepath.ToSlash(relative))
	}

	slices.Sort(workflows)

	return slices.Compact(workflows)
}

func jobRunsStep(target job, text string) bool {
	return slices.ContainsFunc(target.steps(), func(step ast.Node) bool {
		return strings.Contains(scalarString(mappingValue(step, "run")), text)
	})
}

// A job that builds container images runs on the container runners, lent or
// the installation's own, never on the plain pool, and it waits for the job
// that requested a lent runner. The request job itself runs on the pool: it
// is what makes the lent runner exist.
func TestContainerBuildingJobsRunOnContainerRunnersAfterRequestingOne(t *testing.T) {
	t.Parallel()

	for _, relative := range forgejoExampleWorkflows(t) {
		parsed := loadWorkflow(t, relative)

		requesters := make([]string, 0, 1)

		for name, target := range parsed.jobs {
			if jobRunsStep(target, requestRunnerVerb) {
				requesters = append(requesters, name)

				if !strings.Contains(target.runsOn(), "vars.CI_RUNNER_LABEL)") {
					t.Errorf("%s: job %s requests a runner but does not run on the pool (CI_RUNNER_LABEL): %q", relative, name, target.runsOn())
				}
			}
		}

		for name, target := range parsed.jobs {
			builds := slices.ContainsFunc(target.steps(), func(step ast.Node) bool {
				return containerBuildingActions.MatchString(scalarString(mappingValue(step, "uses")))
			})
			if !builds {
				continue
			}

			if !strings.Contains(target.runsOn(), containerRunnerVariable) {
				t.Errorf("%s: job %s runs Buildah on %q, want a %s runner", relative, name, target.runsOn(), containerRunnerVariable)
			}

			if !slices.ContainsFunc(jobNeeds(target), func(need string) bool { return slices.Contains(requesters, need) }) {
				t.Errorf("%s: job %s builds images without waiting for a job that runs `%s`", relative, name, requestRunnerVerb)
			}
		}
	}
}

// The lender's token is the request job's only secret. It reaches the engine
// over stdin and is cleared from the environment the engine inherits, so no
// argv and no child process carries it.
func TestRequestRunnerStepsPassTheLenderTokenOverStdinOnly(t *testing.T) {
	t.Parallel()

	secret := regexp.MustCompile(`secrets\.[A-Za-z0-9_]+`)

	found := 0

	for _, relative := range forgejoExampleWorkflows(t) {
		parsed := loadWorkflow(t, relative)

		for name, target := range parsed.jobs {
			if !jobRunsStep(target, requestRunnerVerb) {
				continue
			}

			found++

			anyString(target.node, func(value string) bool {
				for _, reference := range secret.FindAllString(value, -1) {
					if reference != "secrets.CI_RUNNER_LENDER_TOKEN" {
						t.Errorf("%s: job %s reads %s; a request job holds the lender token and nothing else", relative, name, reference)
					}
				}

				return false
			})

			for _, step := range target.steps() {
				run := scalarString(mappingValue(step, "run"))
				if !strings.Contains(run, requestRunnerVerb) {
					continue
				}

				if !strings.Contains(run, `printf '%s' "${CI_RUNNER_LENDER_TOKEN}" |`) || !strings.Contains(run, "CI_RUNNER_LENDER_TOKEN=''") || !strings.Contains(run, "--token-file -") {
					t.Errorf("%s: job %s must pipe the lender token to --token-file - and clear it from the engine's environment:\n%s", relative, name, run)
				}
			}
		}
	}

	if found == 0 {
		t.Fatal("no example requests a runner; the guard has drifted")
	}
}

// The lender workflow has no authority on GitHub and uses no action: it fetches
// the pinned engine's lending script without credentials and runs it with the
// registration token, the one secret. A request chooses an architecture and a
// wait; the labels the runner registers with are this repository's own.
func TestLenderExampleLendsWithoutGitHubAuthorityOrForeignActions(t *testing.T) {
	t.Parallel()

	document := loadDocument(t, lenderExample)
	parsed := loadWorkflow(t, lenderExample)

	if permissions := mappingValue(document, "permissions"); permissions == nil || len(mappingEntries(permissions)) != 0 {
		t.Error("the lender needs nothing from GitHub: permissions must be the empty mapping")
	}

	dispatch := mappingValue(mappingValue(document, "on"), "workflow_dispatch")
	inputs := mappingValue(dispatch, "inputs")

	options := make([]string, 0, 2)
	for _, option := range sequenceValues(mappingValue(mappingValue(inputs, "arch"), "options")) {
		options = append(options, scalarString(option))
	}

	if !slices.Equal(options, []string{"amd64", "arm64"}) || scalarString(mappingValue(mappingValue(inputs, "arch"), "type")) != "choice" {
		t.Errorf("arch must be a choice of amd64 and arm64, got %v", options)
	}

	if mappingValue(inputs, "labels") != nil {
		t.Error("labels are the lender's configuration, never a request input")
	}

	if len(parsed.jobs) != 1 {
		t.Fatalf("the lender has %d jobs, want one", len(parsed.jobs))
	}

	for name, target := range parsed.jobs {
		if got := target.runsOn(); got != "${{ inputs.arch == 'arm64' && 'ubuntu-26.04-arm' || 'ubuntu-26.04' }}" {
			t.Errorf("job %s selects its VM by %q, want the hosted label of the requested architecture", name, got)
		}

		if !anyString(target.node, func(v string) bool { return v == "gh-vm,arch=${{ inputs.arch }}" }) {
			t.Errorf("job %s must register with the lender's own labels, gh-vm and the architecture", name)
		}

		for _, step := range target.steps() {
			if uses := scalarString(mappingValue(step, "uses")); uses != "" {
				t.Errorf("job %s uses %q; the lender runs no action", name, uses)
			}
		}

		if !jobRunsStep(target, "git checkout --quiet --detach FETCH_HEAD") || !jobRunsStep(target, `test "$(git rev-parse HEAD)" = "${ENGINE_REF}"`) {
			t.Errorf("job %s must fetch the engine without credentials and verify the pinned commit", name)
		}

		if !jobRunsStep(target, "sudo --preserve-env=FORGEJO_INSTANCE_URL,FORGEJO_RUNNER_REGISTRATION_TOKEN,RUNNER_LABELS,LEND_TTL_MINUTES") || !jobRunsStep(target, "bash "+lendScript) {
			t.Errorf("job %s must run %s with exactly the lending environment preserved", name, lendScript)
		}
	}

	secret := regexp.MustCompile(`secrets\.[A-Za-z0-9_]+`)

	anyString(document, func(value string) bool {
		for _, reference := range secret.FindAllString(value, -1) {
			if reference != "secrets.FORGEJO_RUNNER_REGISTRATION_TOKEN" {
				t.Errorf("the lender reads %s; the registration token is its one secret", reference)
			}
		}

		return false
	})
}

// The lending script registers an ephemeral runner as a dedicated unprivileged
// user under a clean environment, so the job it hosts sees neither the
// credentials of the process that started the machine nor root.
func TestLendScriptRegistersEphemeralAsAnUnprivilegedUserWithACleanEnvironment(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), lendScript))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"register --no-interactive --ephemeral",
		"one-job --wait",
		`sudo -u "$LENDER_USER" env -i`,
		"useradd --create-home",
		// The TTL bounds the wait for a job, never a job that has started.
		`[[ -d "${RUNNER_DIR}/work" ]]`,
		`kill -INT "$runner_pid"`,
		"install-forgejo-runner.sh",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("%s lacks %q", lendScript, want)
		}
	}
}
