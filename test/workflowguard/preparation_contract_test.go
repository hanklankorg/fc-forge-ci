// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"strings"
)

const preparedBumpPlanRun = `forge-ci version bump-plan --patch "$RUNNER_TEMP/prepared-version-bumps/native.patch" --patch-sha256 "$PATCH_DIGEST"`

func preparationViolations(body []byte) []string {
	doc, err := effectiveWorkflow(body)
	if err != nil {
		return []string{err.Error()}
	}

	job := mappingChild(mappingChild(doc, "jobs"), "version-bump")
	if job == nil {
		return []string{"missing version-bump job"}
	}

	needs := mappingChild(job, "needs")
	if needs == nil || needs.Kind != yaml.SequenceNode || len(needs.Content) != 4 || needs.Content[0].Value != "resolve-bootstrap" || needs.Content[1].Value != "generate-full-changelog" || needs.Content[2].Value != "generate-minimal-changelog" || needs.Content[3].Value != "prepare-version-files" {
		return []string{"preparation needs both changelog jobs and the key-free version job"}
	}

	if scalarChild(job, "if") != "${{ !cancelled() && needs.resolve-bootstrap.result == 'success' && needs.generate-full-changelog.result != 'failure' && needs.generate-minimal-changelog.result != 'failure' && needs.prepare-version-files.result != 'failure' && needs.prepare-version-files.result != 'cancelled' }}" {
		return []string{"preparation job failure condition changed"}
	}

	preparation := mappingChild(mappingChild(doc, "jobs"), "prepare-version-files")
	prepareSteps := mappingChild(preparation, "steps")
	nativeRuns := 0

	if prepareSteps != nil {
		for _, step := range prepareSteps.Content {
			if scalarChild(step, "run") == "forge-ci version bump-plan" {
				nativeRuns++
			}
		}
	}

	if nativeRuns != 1 {
		return []string{"native version plan must run once in the key-free job"}
	}

	steps := mappingChild(job, "steps")
	if steps == nil {
		return []string{"preparation steps missing"}
	}

	const condition = "${{ inputs['existing-release-sha'] == '' && fromJson(inputs['prepare-stage-plan-json']).targets.version_bump.runs }}"

	// The push target is passed explicitly: the flag has no environment source.
	wanted := []string{preparedBumpPlanRun, `forge-ci version commit-push --branch "$BRANCH"`, "forge-ci version tag-release"}

	var (
		got        []string
		failures   []string
		bumpPlanID string
	)

	for _, step := range steps.Content {
		run := strings.TrimSpace(scalarChild(step, "run"))
		for _, command := range wanted {
			if run != command {
				continue
			}

			got = append(got, command)

			if command == wanted[0] {
				bumpPlanID = scalarChild(step, "id")
			}

			wantIf := condition
			if command == wanted[2] {
				wantIf = ""
			}

			if scalarChild(step, "if") != wantIf {
				failures = append(failures, "incorrect condition: "+command)
			}

			if command == wanted[1] {
				failures = append(failures, commitStepViolations(step, bumpPlanID)...)
			}
		}
	}

	if strings.Join(got, "\n") != strings.Join(wanted, "\n") {
		failures = append(failures, fmt.Sprintf("preparation commands/order = %v", got))
	}

	return failures
}

// commitStepViolations checks what the release commit step is authorized by and
// what it stages.
//
// The pathspec handoff is the one worth spelling out. The commit stages what
// bump-plan said it changed, and nothing else, and the only thing binding those
// two halves together is a single env expression. A literal pattern here, or a
// reference to another step's output, would commit a set nobody computed — and
// the workflow would run green, because a wrong pathspec is still a valid one.
// bumpPlanID is populated by the time this runs because bump-plan is required
// to come first.
func commitStepViolations(step *yaml.Node, bumpPlanID string) []string {
	var failures []string

	env := mappingChild(step, "env")

	if scalarChild(env, "AUTHORIZED_SOURCE_SHA") != "${{ inputs['authorized-source-sha'] }}" {
		failures = append(failures, "commit must lease-check authorized source")
	}

	if bumpPlanID == "" {
		return append(failures, "bump-plan must carry an id so its file-pattern output can be referenced")
	}

	if scalarChild(env, "FILE_PATTERN") != "${{ steps."+bumpPlanID+".outputs.file-pattern }}" {
		failures = append(failures, "commit must stage exactly the bump-plan file-pattern output")
	}

	return failures
}
