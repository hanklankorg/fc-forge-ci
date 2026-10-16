// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"
)

// actionSteps returns the steps of the composite action at path.
func actionSteps(t *testing.T, path string) []ast.Node {
	t.Helper()

	runs := mappingValue(loadDocument(t, path), "runs")
	if scalarString(mappingValue(runs, "using")) != "composite" {
		t.Fatalf("%s is not a composite action", path)
	}

	steps := sequenceValues(mappingValue(runs, "steps"))
	if len(steps) == 0 {
		t.Fatalf("%s has no steps", path)
	}

	return steps
}

// A relative `uses:` inside a composite action resolves against the calling
// job's workspace, which holds none of this layer's files. An action therefore
// never calls a sibling by path: it runs the engine itself, as
// build-release-image does for its three calls. The only `uses:` an action
// carries is a full URL.
func TestCompositeActionsCallNoSiblingByPath(t *testing.T) {
	t.Parallel()

	references := 0

	for _, path := range layerActions(t) {
		for _, step := range actionSteps(t, path) {
			uses := scalarString(mappingValue(step, "uses"))
			if uses == "" {
				continue
			}

			references++

			if !strings.HasPrefix(uses, "https://") {
				t.Errorf("%s calls %q, which the runner resolves against the caller's workspace", path, uses)
			}
		}
	}

	// The walk reaches known content: setup-toolchain calls two actions by URL.
	if references == 0 {
		t.Fatal("found no `uses:` in any action, so the absence check proves nothing")
	}
}

// buildAndCheckActions are the actions a consumer runs in a pull-request job
// or in its own build job, where no credential of the layer belongs.
func buildAndCheckActions() []string {
	return []string{"lint-nanolinter", "build-go", "build-cargo", "build-maven", "build-release-image"}
}

// The lint, build and image actions take no token, password or key: nothing
// in their inputs or their steps names one, and each step's variables come
// from inputs, from an earlier step, or from the two public facts of the run.
// The registry credential of an image build stays in the auth file the
// consumer's registry-login step wrote; these actions pass its path.
func TestBuildAndCheckActionsReceiveNoCredential(t *testing.T) {
	t.Parallel()

	credential := func(value string) bool {
		lower := strings.ToLower(value)

		return strings.Contains(lower, "secrets.") || strings.Contains(lower, "token") ||
			strings.Contains(lower, "password") || strings.Contains(lower, "signing")
	}

	for _, name := range buildAndCheckActions() {
		path := ".forgejo/actions/" + name + "/action.yml"
		document := loadDocument(t, path)

		for _, input := range mappingEntries(mappingValue(document, "inputs")) {
			if credential(scalarString(input.Key)) {
				t.Errorf("%s declares the credential input %s", path, scalarString(input.Key))
			}
		}

		bound := 0

		for _, step := range actionSteps(t, path) {
			if scalarString(mappingValue(step, "uses")) != "" {
				t.Errorf("%s step %q calls another action", path, scalarString(mappingValue(step, "name")))
			}

			for _, entry := range mappingEntries(mappingValue(step, "env")) {
				bound++

				value := scalarString(entry.Value)
				switch {
				case credential(value):
					t.Errorf("%s binds %s to %s", path, scalarString(entry.Key), value)
				case strings.HasPrefix(value, "${{ inputs."), strings.HasPrefix(value, "${{ steps."),
					value == "${{ github.server_url }}", value == "${{ github.repository }}":
				default:
					t.Errorf("%s binds %s to %s, which is neither an input, a step output nor the run's repository", path, scalarString(entry.Key), value)
				}
			}
		}

		if bound == 0 {
			t.Fatalf("%s binds no step variable, so the walk proves nothing", path)
		}
	}
}

// The pull-request examples are what a consumer copies for the event that
// runs unreviewed code: each reads no secret anywhere, names no image of its
// own (the job image is the consumer's variable), and its three jobs are the
// lint action, the isolation gate and its ecosystem's build action.
func TestConsumerPRTemplateIsTheLintGateAndBuild(t *testing.T) {
	t.Parallel()

	for _, kit := range consumerKits() {
		checkConsumerPRTemplate(t, kit)
	}
}

func checkConsumerPRTemplate(t *testing.T, kit consumerKit) {
	t.Helper()

	path := kit.pullRequest()

	document := loadDocument(t, path)
	if mentions(document, "secrets.") {
		t.Errorf("%s reads a secret", path)
	}

	requireWalkReaches(t, document, "vars.CI_RUNNER_LABEL", path)

	const jobImage = "${{ vars.CI_JOB_IMAGE }}"

	parsed := loadWorkflow(t, path)

	for name, want := range map[string]struct{ action, image string }{
		"lint":           {"/.forgejo/actions/lint-nanolinter@", jobImage},
		"trust-boundary": {"/.forgejo/actions/check-l3-isolation@", ""},
		"build":          {"/.forgejo/actions/" + kit.buildAction + "@", jobImage},
	} {
		entry, ok := parsed.jobs[name]
		if !ok {
			t.Errorf("%s has no %s job", path, name)

			continue
		}

		if got := entry.containerImage(); got != want.image {
			t.Errorf("%s job %s runs in image %q, want %q", path, name, got, want.image)
		}

		called := false
		for _, step := range entry.steps() {
			called = called || strings.Contains(scalarString(mappingValue(step, "uses")), want.action)
		}

		if !called {
			t.Errorf("%s job %s does not call %s", path, name, want.action)
		}
	}

	if len(parsed.jobs) != 3 {
		t.Errorf("%s has %d jobs, want the three above", path, len(parsed.jobs))
	}
}

// Each release example's build job is a complete build, not a stub: it builds
// with its ecosystem's build action from the prepared release's tag and
// commit, declares its files, and hands dist/ off with the digest the signer
// checks.
func TestConsumerReleaseTemplateBuildsWithTheBuildAction(t *testing.T) {
	t.Parallel()

	for _, kit := range consumerKits() {
		checkConsumerReleaseBuild(t, kit)
	}
}

func checkConsumerReleaseBuild(t *testing.T, kit consumerKit) {
	t.Helper()

	path := kit.release()

	build, ok := loadWorkflow(t, path).jobs["build"]
	if !ok {
		t.Errorf("%s has no build job", path)

		return
	}

	var order []string

	for _, step := range build.steps() {
		uses, run := scalarString(mappingValue(step, "uses")), scalarString(mappingValue(step, "run"))

		switch {
		case strings.Contains(uses, "/.forgejo/actions/"+kit.buildAction+"@"):
			with := mappingValue(step, "with")
			if got := scalarString(mappingValue(with, "version")); got != "${{ needs.prepare.outputs.release-tag }}" {
				t.Errorf("%s: the build is stamped with version %q, not the prepared release tag", path, got)
			}

			if got := scalarString(mappingValue(with, "commit")); got != "${{ needs.prepare.outputs.release-sha }}" {
				t.Errorf("%s: the build is stamped with commit %q, not the prepared release commit", path, got)
			}

			order = append(order, "build")
		case strings.Contains(run, "release files declare"):
			order = append(order, "declare")
		case strings.Contains(uses, "/.forgejo/actions/upload-dist@"):
			order = append(order, "upload")
		}

		if strings.Contains(run, "exit 1") {
			t.Errorf("%s: build step %q still stops for the consumer to fill in", path, scalarString(mappingValue(step, "name")))
		}
	}

	if strings.Join(order, ",") != "build,declare,upload" {
		t.Errorf("%s: the build job runs %q, want build, declare, upload", path, order)
	}
}
