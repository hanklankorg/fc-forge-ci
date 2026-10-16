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

// The version bump runs the project's build tool, so it runs in a job of its
// own that references no secret at all, not even a presence check. The signing
// job waits for it, takes only its patch and the patch's digest, and runs no
// build tool: it verifies the patch with apply-bump and commits the files that
// verification names.
func TestPrepareReleaseIsolatesTheVersionBumpFromTheSigningJob(t *testing.T) {
	t.Parallel()

	parsed := loadWorkflow(t, ".forgejo/workflows/prepare-release.yml")
	if !waitsFor(parsed, "prepare", "version-bump") {
		t.Fatal("signing must wait for the separate version-bump job")
	}

	bump, found := parsed.jobs["version-bump"]
	if !found {
		t.Fatal("prepare-release.yml has no version-bump job")
	}

	anyString(bump.node, func(value string) bool {
		if secretReference.MatchString(value) {
			t.Errorf("the version-bump job references a secret: %s", value)
		}

		return false
	})

	if !mentions(bump.node, "version bump --project-type") || !mentions(bump.node, "artifact upload") {
		t.Error("the version-bump job must run the engine's bump and upload its patch")
	}

	prepare := parsed.jobs["prepare"]
	for _, forbidden := range []string{"version bump ", "versions:set", "mvn", "version-bump-image", "version_bump_image"} {
		if mentions(prepare.node, forbidden) {
			t.Errorf("the signing job mentions %q: it may verify a bump, never run one", forbidden)
		}
	}

	if !mentions(prepare.node, "version apply-bump") || !mentions(prepare.node, "needs.version-bump.outputs.patch-digest") ||
		!mentions(prepare.node, "steps.bump.outputs.paths-file") {
		t.Error("the signing job must verify the patch against its job's digest and commit only what apply-bump names")
	}
}
