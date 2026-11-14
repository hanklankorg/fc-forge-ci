// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"strings"
	"testing"
)

// A config hook can persist across steps. Only the job boundary separates
// project-controlled rendering from the job that later receives the SSH key.
func TestPrepareReleaseIsolatesRenderingFromTheSigningJob(t *testing.T) {
	t.Parallel()

	parsed := loadWorkflow(t, ".forgejo/workflows/prepare-release.yml")
	if !waitsFor(parsed, "prepare", "render") {
		t.Fatal("signing must wait for a separate render job")
	}

	render := parsed.jobs["render"]
	for _, step := range render.steps() {
		anyString(step, func(value string) bool {
			if secretReference.MatchString(value) && !strings.HasSuffix(value, " != '' }}") {
				t.Errorf("render job receives a secret value: %s", value)
			}

			return false
		})
	}

	prepare := parsed.jobs["prepare"]
	if mentions(prepare.node, "inputs['changelog-") || mentions(prepare.node, "inputs['commit-body-config']") {
		t.Error("the signing job must never evaluate a renderer config")
	}

	for _, step := range prepare.steps() {
		uses := scalarString(mappingValue(step, "uses"))
		if uses != "" && uses != "./.forge-ci-helpers/.forgejo/actions/prepare-release" &&
			uses != "./.forge-ci-helpers/.forgejo/actions/guard-release-tag" {
			t.Errorf("the signing job runs an action outside its signing boundary: %s", uses)
		}
	}

	signing := loadDocument(t, ".forgejo/actions/prepare-release/action.yml")
	if mentions(signing, "render-changelog") || mentions(signing, "install-changelog-renderer") {
		t.Error("the signing action runs a renderer in the key-holding job")
	}

	if !mentions(render.node, "artifact upload") || !mentions(prepare.node, "artifact download") ||
		!mentions(prepare.node, `cp --remove-destination -- "${handoff}/${file}" "${file}"`) ||
		!mentions(prepare.node, "for file in CHANGELOG.md commit-msg.txt;") ||
		!mentions(prepare.node, `[ -L "${handoff}/${file}" ]`) {
		t.Error("only the two regular rendered data files may cross into the fresh signing checkout")
	}
}

// The plan decides the version bump, and a family's native version tool runs
// the project's plugins, so each family's bump runs in a version job of its
// own that references no secret at all, not even a presence check. The plan
// job reads the declaration as data and names no secret either. The signing
// job waits for the version jobs, takes only their patches and digests, and
// runs no build tool: it checks each patch against its job's digest and
// commits only the paths bump-plan names.
func TestPrepareReleaseIsolatesTheVersionBumpFromTheSigningJob(t *testing.T) {
	t.Parallel()

	parsed := loadWorkflow(t, ".forgejo/workflows/prepare-release.yml")
	if !waitsFor(parsed, "prepare", "version") || !waitsFor(parsed, "version", "plan") {
		t.Fatal("signing must wait for the version jobs, and they for the plan")
	}

	for _, name := range []string{"plan", "version"} {
		target, found := parsed.jobs[name]
		if !found {
			t.Fatalf("prepare-release.yml has no %s job", name)
		}

		anyString(target.node, func(value string) bool {
			if secretReference.MatchString(value) {
				t.Errorf("the %s job references a secret: %s", name, value)
			}

			return false
		})
	}

	if !mentions(parsed.jobs["plan"].node, "plan release-versions") || mentions(parsed.jobs["plan"].node, "version bump-plan") {
		t.Error("the plan job must plan with the engine and bump nothing")
	}

	version := parsed.jobs["version"]
	if !mentions(version.node, "version bump-plan") || !mentions(version.node, "--project-type") ||
		!mentions(version.node, "artifact upload") || !mentions(version.node, "fromJSON(needs.plan.outputs.versions") {
		t.Error("the version jobs must run the engine's bump of their planned family and upload its patch")
	}

	prepare := parsed.jobs["prepare"]
	for _, forbidden := range []string{"--project-type", "versions:set", "mvn", "runtime_image", "runtime-image"} {
		if mentions(prepare.node, forbidden) {
			t.Errorf("the signing job mentions %q: it may verify a bump, never run one", forbidden)
		}
	}

	for _, family := range []string{"java", "node", "rust"} {
		if !mentions(prepare.node, "needs.version.outputs.patch_digest_"+family) {
			t.Errorf("the signing job does not check the %s patch against its job's digest", family)
		}
	}

	if !mentions(prepare.node, "--patch-sha256") || !mentions(prepare.node, "--paths-file") ||
		!mentions(prepare.node, "steps.bump.outputs.paths-file") {
		t.Error("the signing job must verify the joined patch and commit only what bump-plan names")
	}
}

// Forgejo decides on the server, before any runner evaluates a condition,
// that the dependant of a skipped job is skipped too (a consumer fixture, lab
// runs 65 and 67, 2026-10-16), and an empty matrix blocks its dependants
// (forgejo/forgejo#12191). So the version job is never skipped: a plan
// without a family runs one empty row, each step but the note is conditioned
// on the row's family, and the signing job needs it on its success alone.
func TestPrepareReleaseGoesOnWithoutAVersionBump(t *testing.T) {
	t.Parallel()

	parsed := loadWorkflow(t, ".forgejo/workflows/prepare-release.yml")
	for _, name := range []string{"plan", "version", "prepare"} {
		want := needsGate(jobNeeds(parsed.jobs[name]))
		if condition := scalarString(mappingValue(parsed.jobs[name].node, "if")); condition != want {
			t.Errorf("job %s runs if %q, want its needs' success alone (%q): any other condition could skip the job on the server before any runner",
				name, condition, want)
		}
	}

	version := parsed.jobs["version"]

	matrix := scalarString(mappingValue(mappingValue(mappingValue(version.node, "strategy"), "matrix"), "version"))
	if want := "${{ fromJSON(needs.plan.outputs.versions != '[]' && needs.plan.outputs.versions || '[{}]') }}"; matrix != want {
		t.Errorf("the version matrix is %q, want %q", matrix, want)
	}

	const stepCondition = "${{ matrix.version.family != '' }}"

	steps := version.steps()
	if len(steps) < 2 || scalarString(mappingValue(steps[0], "if")) != "${{ !matrix.version.family }}" {
		t.Fatal("the version job's first step must be the empty row's note")
	}

	for _, step := range steps[1:] {
		if condition := scalarString(mappingValue(step, "if")); condition != stepCondition {
			t.Errorf("version step %q must run only for a planned family: if %q, want %q",
				scalarString(mappingValue(step, "name")), condition, stepCondition)
		}
	}

	bump, found := parsed.jobs["prepare"].stepByID("bump")
	if !found || scalarString(mappingValue(bump, "if")) != "" {
		t.Error("the signing job must verify the plan's bump whether or not it has a family")
	}
}
