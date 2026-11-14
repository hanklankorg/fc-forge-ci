// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"slices"
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
// never calls a sibling by path: it runs the engine itself. The only `uses:`
// an action carries is a full URL.
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
	return []string{"lint-nanolinter", "build-go", "build-cargo", "build-maven"}
}

// The lint and build actions take no token, password or key: nothing
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
// own (the job image is the consumer's variable), and its jobs are the lint
// action, the isolation gate, its ecosystem's build (its build action, or
// the build tool where the layer has none) and the summary.
func TestConsumerPRTemplateIsTheLintGateAndBuild(t *testing.T) {
	t.Parallel()

	for _, kit := range consumerKits() {
		checkConsumerPRTemplate(t, kit)
	}
}

func checkConsumerPRTemplate(t *testing.T, kit consumerKit) {
	t.Helper()

	if err := kit.validate(); err != nil {
		t.Fatal(err)
	}

	path := kit.pullRequest()

	document := loadDocument(t, path)
	if mentions(document, "secrets.") {
		t.Errorf("%s reads a secret", path)
	}

	requireWalkReaches(t, document, "vars.CI_RUNNER_LABEL", path)

	// A per-project pull request has a check job for each project, not the
	// four jobs below.
	if kit.perProject {
		return
	}

	const jobImage = "${{ vars.CI_JOB_IMAGE }}"

	parsed := loadWorkflow(t, path)

	usesAction := func(action string) func(ast.Node) bool {
		return func(step ast.Node) bool {
			return strings.Contains(scalarString(mappingValue(step, "uses")), "/.forgejo/actions/"+action+"@")
		}
	}

	build, buildName := usesAction(kit.buildAction), kit.buildAction
	if kit.buildAction == "" {
		build = func(step ast.Node) bool { return runsOnly(runScript(step), kit.prBuild) }
		buildName = kit.prBuild
	}

	for name, want := range map[string]struct {
		calls   string
		image   string
		matches func(ast.Node) bool
	}{
		"lint":           {"lint-nanolinter", jobImage, usesAction("lint-nanolinter")},
		"trust-boundary": {"check-l3-isolation", "", usesAction("check-l3-isolation")},
		"build":          {buildName, jobImage, build},
		"summary":        {"setup-forge-ci", "", usesAction("setup-forge-ci")},
	} {
		entry, ok := parsed.jobs[name]
		if !ok {
			t.Errorf("%s has no %s job", path, name)

			continue
		}

		if got := entry.containerImage(); got != want.image {
			t.Errorf("%s job %s runs in image %q, want %q", path, name, got, want.image)
		}

		if !slices.ContainsFunc(entry.steps(), want.matches) {
			t.Errorf("%s job %s does not call %s", path, name, want.calls)
		}
	}

	if len(parsed.jobs) != 4 {
		t.Errorf("%s has %d jobs, want the four above", path, len(parsed.jobs))
	}
}

// Each release example's build job is a complete build, not a stub: it builds
// with its ecosystem's build action from the prepared release's tag and
// commit (or with the engine's build where the layer has no action),
// declares its files, and hands dist/ off with the digest the signer
// checks.
func TestConsumerReleaseTemplateBuildsWithTheBuildAction(t *testing.T) {
	t.Parallel()

	for _, kit := range consumerKits() {
		checkConsumerReleaseBuild(t, kit)
	}
}

func TestGoReleaserCallerDelegatesPackagingAndUnsignedHandoff(t *testing.T) {
	t.Parallel()

	workflow := loadWorkflow(t, "examples/go-cli/forgejo/release-goreleaser.yml")
	if len(workflow.jobs) != 3 {
		t.Fatalf("GoReleaser starter has %d jobs, want prepare, build and sign/publish", len(workflow.jobs))
	}

	build := workflow.jobs["build"]

	var order []string

	for _, step := range build.steps() {
		uses := scalarString(mappingValue(step, "uses"))
		if strings.Contains(uses, "/build-goreleaser@") {
			with := mappingValue(step, "with")
			for name, expected := range map[string]string{
				"release-tag":   "${{ needs.prepare.outputs.release-tag }}",
				"release-sha":   "${{ needs.prepare.outputs.release-sha }}",
				"release-notes": "release-notes.md",
			} {
				if got := scalarString(mappingValue(with, name)); got != expected {
					t.Errorf("GoReleaser %s = %q, want %q", name, got, expected)
				}
			}

			order = append(order, "package")
		}

		if strings.Contains(uses, "/upload-dist@") {
			order = append(order, "upload")
		}

		for _, copied := range []string{"goreleaser release", "files import-goreleaser", "artifact upload"} {
			if strings.Contains(scalarString(mappingValue(step, "run")), copied) {
				t.Errorf("starter copies shared operation %q instead of using the existing action", copied)
			}
		}
	}

	if strings.Join(order, ",") != "package,upload" {
		t.Errorf("GoReleaser handoff order = %q, want package then upload", order)
	}

	if anyString(build.node, secretReference.MatchString) {
		t.Error("the locked public-tool build starter must not receive signing/publication credentials")
	}
}

// The GoReleaser example ships distro packages whole: its configuration
// builds the three formats, both of its calls turn package signing on, and
// the declaration beside it publishes them. One of the three alone would be
// packages without a signature, or a signing step with nothing to sign.
func TestGoReleaserExampleBuildsSignsAndDeclaresDistroPackages(t *testing.T) {
	t.Parallel()

	workflow := loadWorkflow(t, "examples/go-cli/forgejo/release-goreleaser.yml")

	for _, name := range []string{"prepare", "sign-and-publish"} {
		if got := scalarString(mappingValue(mappingValue(workflow.jobs[name].node, "with"), "sign-distro-packages")); got != "true" {
			t.Errorf("job %s passes sign-distro-packages %q, want true", name, got)
		}
	}

	var formats []string

	for _, packager := range sequenceValues(mappingValue(loadDocument(t, "examples/go-cli/.goreleaser.yml"), "nfpms")) {
		for _, format := range sequenceValues(mappingValue(packager, "formats")) {
			formats = append(formats, scalarString(format))
		}
	}

	slices.Sort(formats)

	if !slices.Equal(formats, []string{"apk", "deb", "rpm"}) {
		t.Errorf(".goreleaser.yml packages %v, want apk, deb and rpm", formats)
	}

	if mappingValue(loadDocument(t, "examples/go-cli/artifacts-forgejo-packages.yml"), "distro-packages") == nil {
		t.Error("artifacts-forgejo-packages.yml declares no distro-packages block")
	}
}

func TestServiceReleaseFanInUsesTheExistingAssembler(t *testing.T) {
	t.Parallel()

	workflow := loadWorkflow(t, "examples/go-service/forgejo/release.yml")
	handoff := workflow.jobs["handoff"]
	steps := handoff.steps()

	if len(steps) != 1 {
		t.Fatalf("handoff has %d steps, want one existing assembler action", len(steps))
	}

	step := steps[0]
	if got := scalarString(mappingValue(step, "uses")); got != "https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/assemble-release-dist@PIN_FORGE_CI_SHA" {
		t.Fatalf("handoff calls %q, want the pinned shared assembler", got)
	}

	with := mappingValue(step, "with")

	names, ok := mappingValue(with, "artifact-names").(*ast.LiteralNode)
	if !ok {
		t.Fatal("handoff must enumerate its artifact names as a literal block")
	}

	if got := strings.Fields(names.Value.Value); strings.Join(got, ",") != "release-files,release-images" {
		t.Errorf("handoff artifact set = %q, want release files and image evidence", got)
	}

	if got := scalarString(mappingValue(mappingValue(handoff.node, "outputs"), "dist_digest")); got != "${{ steps.dist.outputs.digest }}" {
		t.Errorf("handoff digest output = %q, want the assembler digest", got)
	}

	if anyString(handoff.node, secretReference.MatchString) {
		t.Error("release assembly must not receive signing or publication secrets")
	}
}

func checkConsumerReleaseBuild(t *testing.T, kit consumerKit) {
	t.Helper()

	if err := kit.validate(); err != nil {
		t.Fatal(err)
	}

	// A per-project release has a build job for each project and one that
	// joins them, not the one build job held here.
	if kit.perProject {
		return
	}

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
		case kit.buildAction == "" && runsOnly(runScript(step), kit.releaseBuild):
			// The engine's build reads the version the checked-out release
			// commit declares, so it takes no version or commit to stamp.
			order = append(order, "build")
		case kit.buildAction != "" && strings.Contains(uses, "/.forgejo/actions/"+kit.buildAction+"@"):
			with := mappingValue(step, "with")
			if got := scalarString(mappingValue(with, "version")); got != "${{ needs.prepare.outputs.release-tag }}" {
				t.Errorf("%s: the build is stamped with version %q, not the prepared release tag", path, got)
			}

			if got := scalarString(mappingValue(with, "commit")); got != "${{ needs.prepare.outputs.release-sha }}" {
				t.Errorf("%s: the build is stamped with commit %q, not the prepared release commit", path, got)
			}

			// One build step a project, all before the declaration.
			if len(order) == 0 || order[len(order)-1] != "build" {
				order = append(order, "build")
			}
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
