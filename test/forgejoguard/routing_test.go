// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"
)

// These rules pin consumer-facing routing: every job runs on the runner the
// consumer selected, and a release fails before any checkout when a secret it
// will need is missing.

const (
	reusableRunner        = "${{ fromJSON(inputs['runs-on']) }}"
	signerRunner          = "${{ fromJSON(inputs['signer-runs-on']) }}"
	repositoryRunner      = "${{ fromJSON(vars.CI_RUNNER_LABEL) }}"
	repositoryRunnerInput = "${{ vars.CI_RUNNER_LABEL }}"
	// containerRunner is where a job that builds container images runs: the
	// repository's second reviewed runner variable when set, because that job
	// needs user namespaces the plain pool may deny, and the pool otherwise.
	// runnerlending_test.go holds the converse, that only such a job uses it.
	containerRunner      = "${{ fromJSON(vars.CI_RUNNER_LABEL_CONTAINER || vars.CI_RUNNER_LABEL) }}"
	containerRunnerInput = "${{ vars.CI_RUNNER_LABEL_CONTAINER || vars.CI_RUNNER_LABEL }}"
	// runtimeSmokeRunner picks the input by architecture and never one
	// input's value in place of another's.
	runtimeSmokeRunner = "${{ fromJSON(inputs[matrix.arch == 'arm64' && 'runs-on-linux-arm64' || 'runs-on']) }}"
)

// buildRunners are where a layer workflow's per-architecture build runs: the
// build input of its own architecture, a native runner and never CPU
// emulation, with no other input standing in. The snapshot build selects the
// input by key and names the plain one only for the no-op row of a plan
// without an image, which has no architecture and never runs.
var buildRunners = []string{
	"${{ fromJSON(inputs[matrix.build.arch && format('build-runs-on-linux-{0}', matrix.build.arch) || 'runs-on']) }}",
	"${{ fromJSON(inputs[format('build-runs-on-linux-{0}', matrix.build.arch)]) }}",
	"${{ fromJSON(inputs[format('build-runs-on-linux-{0}', matrix.arch)]) }}",
}

// routingViolation reports how a consumer job leaves the repository's runners:
// a reusable-workflow call passes the label as input, a container build runs on
// the container runner, any other job runs on the plain one.
func routingViolation(entry job) string {
	buildsContainers := slices.ContainsFunc(entry.steps(), func(step ast.Node) bool {
		return containerBuildingActions.MatchString(scalarString(mappingValue(step, "uses")))
	})
	if buildsContainers {
		if got := entry.runsOn(); got != containerRunner {
			return "builds container images but runs on " + got
		}

		return ""
	}

	if scalarString(mappingValue(entry.node, "uses")) != "" {
		if mappingValue(entry.node, "runs-on") != nil {
			return "a reusable-workflow caller declares runs-on"
		}

		uses, with := scalarString(mappingValue(entry.node, "uses")), mappingValue(entry.node, "with")

		want := map[string]string{"runs-on": repositoryRunnerInput}
		if strings.Contains(uses, "/.forgejo/workflows/release-images.yml@") {
			want["build-runs-on-linux-amd64"] = containerRunnerInput
		}

		if strings.Contains(uses, "/.forgejo/workflows/sign-and-publish-release.yml@") {
			want["signer-runs-on"] = repositoryRunnerInput
		}

		for _, name := range slices.Sorted(maps.Keys(want)) {
			if got := scalarString(mappingValue(with, name)); got != want[name] {
				return name + " is " + got
			}
		}

		return ""
	}

	if got := entry.runsOn(); got != repositoryRunner {
		return "runs on " + got
	}

	return ""
}

// Each reusable workflow requires the caller's runner input and runs every job
// on one of the caller's inputs, so no job can land on an unreviewed runner. A
// per-architecture build runs on the build input of its architecture, and the
// jobs that read a signing key, and only they, on the signer input.
func TestReusableWorkflowsRunOnTheCallersRunner(t *testing.T) {
	t.Parallel()

	for _, path := range allWorkflows(t) {
		root := loadDocument(t, path)
		inputs := mappingValue(mappingValue(mappingValue(root, "on"), "workflow_call"), "inputs")

		if scalarString(mappingValue(mappingValue(inputs, "runs-on"), "required")) != "true" {
			t.Errorf("%s does not require the runs-on input", path)
		}

		for name, job := range loadWorkflow(t, path).jobs {
			got := job.runsOn()
			// The key itself, not the test whether it is set.
			signs := strings.Contains(job.node.String(), "${{ secrets.RELEASE_COSIGN_PRIVATE_KEY }}") || strings.Contains(job.node.String(), "${{ secrets.RELEASE_GPG_PRIVATE_KEY }}")

			switch {
			case signs != (got == signerRunner):
				t.Errorf("%s job %s runs on %q; a job runs on the signer input exactly when it reads a signing key", path, name, got)
			case got == reusableRunner, got == signerRunner, slices.Contains(buildRunners, got):
			case path == ".forgejo/workflows/runtime-images.yml" && name == "smoke" && got == runtimeSmokeRunner:
			default:
				t.Errorf("%s job %s runs on %q, which is no single runner input of the caller", path, name, got)
			}
		}
	}
}

// Each consumer release template routes every job, direct or reusable,
// through the repository's runner variable.
func TestConsumerReleaseTemplateUsesTheRepositoryRunner(t *testing.T) {
	t.Parallel()

	for _, kit := range consumerKits() {
		for name, job := range loadWorkflow(t, kit.release()).jobs {
			if violation := routingViolation(job); violation != "" {
				t.Errorf("%s job %s: %s", kit.release(), name, violation)
			}
		}
	}
}

// Every job in the consumer pull-request template routes through the
// repository's configured runner label.
func TestConsumerPRTemplateJobsUseTheRepositoryRunner(t *testing.T) {
	t.Parallel()

	for _, kit := range consumerKits() {
		for name, job := range loadWorkflow(t, kit.pullRequest()).jobs {
			if got := job.runsOn(); got != repositoryRunner {
				t.Errorf("%s job %s runs on %q, want %q", kit.pullRequest(), name, got, repositoryRunner)
			}
		}
	}
}

// Every template call to setup-toolchain supplies each required input, and the
// release build keeps its own cache key.
func TestConsumerReleaseTemplateSuppliesSetupToolchainInputs(t *testing.T) {
	t.Parallel()

	var required []string

	for _, entry := range mappingEntries(mappingValue(loadDocument(t, ".forgejo/actions/setup-toolchain/action.yml"), "inputs")) {
		if scalarString(mappingValue(entry.Value, "required")) == "true" {
			required = append(required, scalarString(entry.Key))
		}
	}

	for _, kit := range consumerKits() {
		// A per-project release builds each project in its own image.
		if kit.perProject {
			continue
		}

		calls := 0

		for name, job := range loadWorkflow(t, kit.release()).jobs {
			for _, step := range job.steps() {
				if !strings.Contains(scalarString(mappingValue(step, "uses")), "/actions/setup-toolchain@") {
					continue
				}

				calls++

				with := mappingValue(step, "with")
				for _, input := range required {
					if mappingValue(with, input) == nil {
						t.Errorf("%s job %s omits required setup-toolchain input %s", kit.release(), name, input)
					}
				}

				if got := scalarString(mappingValue(with, "cache-key-suffix")); got != "release-build" {
					t.Errorf("%s job %s uses cache-key-suffix %q, want release-build", kit.release(), name, got)
				}
			}
		}

		if (calls == 0) != kit.noToolchain {
			t.Errorf("%s calls setup-toolchain %d times; a release with a build calls it, and one without does not", kit.release(), calls)
		}
	}
}

// prepare-release checks every secret the consumer template hands it before
// either checkout, so a misconfigured consumer fails before touching code.
func TestPrepareReleaseChecksEverySecretBeforeCheckout(t *testing.T) {
	t.Parallel()

	prepare, ok := loadWorkflow(t, ".forgejo/workflows/prepare-release.yml").jobs["render"]
	if !ok {
		t.Fatal("prepare-release has no render job")
	}

	steps := prepare.steps()
	if len(steps) == 0 || scalarString(mappingValue(steps[0], "name")) != "Check required secrets" {
		t.Fatal("the render job does not open with the required-secret check")
	}

	var checked []string

	for _, entry := range mappingEntries(mappingValue(steps[0], "env")) {
		if name, found := strings.CutPrefix(scalarString(entry.Key), "SECRET_"); found {
			checked = append(checked, name)
		}
	}

	slices.Sort(checked)

	for _, kit := range consumerKits() {
		template, ok := loadWorkflow(t, kit.release()).jobs["prepare"]
		if !ok {
			t.Errorf("%s has no prepare job", kit.release())

			continue
		}

		handed := make([]string, 0, len(mappingEntries(mappingValue(template.node, "secrets"))))
		for _, entry := range mappingEntries(mappingValue(template.node, "secrets")) {
			handed = append(handed, scalarString(entry.Key))
		}

		slices.Sort(handed)

		if len(handed) == 0 || !slices.Equal(checked, handed) {
			t.Errorf("prepare checks secrets %v, but %s hands it %v", checked, kit.release(), handed)
		}
	}
}
