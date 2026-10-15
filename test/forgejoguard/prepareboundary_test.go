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
