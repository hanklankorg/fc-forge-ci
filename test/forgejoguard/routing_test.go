// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"slices"
	"strings"
	"testing"
)

// These rules pin consumer-facing routing: every job runs on the runner the
// consumer selected, and a release fails before any checkout when a secret it
// will need is missing.

const (
	reusableRunner   = "${{ inputs['runner-label'] }}"
	repositoryRunner = "${{ vars.CI_RUNNER_LABEL }}"
)

// routingViolation reports how a consumer job leaves the repository's runner:
// a reusable-workflow call passes the label as input, any other job runs on it.
func routingViolation(entry job) string {
	if scalarString(mappingValue(entry.node, "uses")) != "" {
		if mappingValue(entry.node, "runs-on") != nil {
			return "a reusable-workflow caller declares runs-on"
		}

		if got := scalarString(mappingValue(mappingValue(entry.node, "with"), "runner-label")); got != repositoryRunner {
			return "runner-label is " + got
		}

		return ""
	}

	if got := entry.runsOn(); got != repositoryRunner {
		return "runs on " + got
	}

	return ""
}

// Each reusable workflow requires the caller's runner label and runs every job
// on it, so no job can land on an unreviewed runner.
func TestReusableWorkflowsRunOnTheCallersRunner(t *testing.T) {
	t.Parallel()

	for _, path := range layerWorkflows(t) {
		root := loadDocument(t, path)

		input := mappingValue(mappingValue(mappingValue(mappingValue(root, "on"), "workflow_call"), "inputs"), "runner-label")
		if scalarString(mappingValue(input, "required")) != "true" {
			t.Errorf("%s does not require the runner-label input", path)
		}

		for name, job := range loadWorkflow(t, path).jobs {
			if got := job.runsOn(); got != reusableRunner {
				t.Errorf("%s job %s runs on %q, want %q", path, name, got, reusableRunner)
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

		if calls == 0 {
			t.Errorf("%s no longer calls setup-toolchain", kit.release())
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
