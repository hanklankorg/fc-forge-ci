// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// These are same-workflow symbolic equalities, not expression evaluation.
// Only the typed snapshot planner establishes the build/image matrix relation.
func snapshotLineageProofs(workflow lineageWorkflow, jobName string) map[string]bool {
	proofs := map[string]bool{}
	steps := mappingChild(workflow.jobs[jobName], "steps")
	if steps == nil {
		return proofs
	}
	for _, step := range steps.Content {
		if !strings.HasPrefix(scalarChild(step, "uses"), "actions/download-artifact@") {
			continue
		}
		with := mappingChild(step, "with")
		name, pattern := scalarChild(with, "name"), scalarChild(with, "pattern")
		value, reason := name, fmt.Sprintf("run-time name %q", name)
		if pattern != "" {
			value, reason = pattern, fmt.Sprintf("pattern %q", pattern)
		}
		if snapshotDownloadHasProducer(workflow, jobName, value, pattern != "") {
			proofs[reason] = true
		}
	}
	return proofs
}

func snapshotArtifactNamespace(value string) (string, string) {
	end := strings.Index(value, "}}")
	if end < 0 {
		return "", ""
	}
	match := inputPassthroughPattern.FindStringSubmatch(value[:end+2])
	if match == nil {
		return "", ""
	}
	return match[1], value[end+2:]
}

func snapshotDownloadHasProducer(workflow lineageWorkflow, from, value string, pattern bool) bool {
	namespace, suffix := snapshotArtifactNamespace(value)
	if namespace == "" {
		return false
	}
	uploadSuffix := ""
	switch {
	case !pattern && (suffix == "-inputs" || suffix == "-context"):
		uploadSuffix = suffix
	case pattern && suffix == "-dependency-*":
		uploadSuffix = "-dependency-${{ matrix.artifact.name }}"
	case pattern && suffix == "-receipt-*":
		uploadSuffix = "-receipt-${{ matrix.image.name }}"
	case pattern && suffix == "-layout-${{ matrix.image.name }}-*":
		uploadSuffix = "-layout-${{ matrix.build.name }}-${{ matrix.build.arch }}"
	default:
		return false
	}
	queue, seen := needsOf(workflow.jobs[from]), map[string]bool{from: true}
	for len(queue) > 0 {
		producer := queue[0]
		queue = queue[1:]
		if seen[producer] {
			continue
		}
		seen[producer] = true
		job := workflow.jobs[producer]
		queue = append(queue, needsOf(job)...)
		steps := mappingChild(job, "steps")
		if steps == nil {
			continue
		}
		for _, step := range steps.Content {
			if !strings.HasPrefix(scalarChild(step, "uses"), "actions/upload-artifact@") {
				continue
			}
			input, tail := snapshotArtifactNamespace(scalarChild(mappingChild(step, "with"), "name"))
			if input != namespace || tail != uploadSuffix {
				continue
			}
			switch suffix {
			case "-inputs":
				return true
			case "-context":
				planner := snapshotMatrixPlanner(workflow, from, "build", "builds")
				if planner != "" && snapshotContextPlanner(workflow, producer) == planner {
					return true
				}
			case "-dependency-*":
				planner := snapshotMatrixPlanner(workflow, producer, "artifact", "artifacts")
				if planner != "" && slices.Contains(needsOf(workflow.jobs[from]), planner) {
					return true
				}
			case "-receipt-*":
				if snapshotMatrixPlanner(workflow, producer, "image", "images") != "" {
					return true
				}
			default:
				planner := snapshotMatrixPlanner(workflow, producer, "build", "builds")
				if planner != "" && planner == snapshotMatrixPlanner(workflow, from, "image", "images") {
					return true
				}
			}
		}
	}
	return false
}

var snapshotMatrixExpression = regexp.MustCompile(`^\$\{\{\s*(?i:fromjson)\(needs\.([A-Za-z0-9_-]+)\.outputs\.(builds|images|artifacts)\)\s*\}\}$`)
var snapshotBuildOutput = regexp.MustCompile(`^\$\{\{\s*steps\.([A-Za-z0-9_-]+)\.outputs\.builds\s*\}\}$`)

func snapshotMatrixPlanner(workflow lineageWorkflow, jobName, axis, output string) string {
	job := workflow.jobs[jobName]
	matrix := scalarChild(mappingChild(mappingChild(job, "strategy"), "matrix"), axis)
	match := snapshotMatrixExpression.FindStringSubmatch(matrix)
	if match == nil || match[2] != output || !slices.Contains(needsOf(job), match[1]) {
		return ""
	}
	planner := workflow.jobs[match[1]]
	outputs := mappingChild(planner, "outputs")
	emitter := snapshotBuildOutput.FindStringSubmatch(scalarChild(outputs, "builds"))
	if emitter == nil || scalarChild(outputs, "images") != "${{ steps."+emitter[1]+".outputs.images }}" {
		return ""
	}
	if output == "artifacts" && scalarChild(outputs, "artifacts") != "${{ steps."+emitter[1]+".outputs.artifacts }}" {
		return ""
	}
	steps := mappingChild(planner, "steps")
	if steps != nil {
		for _, step := range steps.Content {
			if scalarChild(step, "id") != emitter[1] {
				continue
			}
			for _, line := range strings.Split(scalarChild(step, "run"), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "forge-ci container snapshot-images plan ") {
					return match[1]
				}
			}
		}
	}
	return ""
}

// A context name is admitted only from the shared gather command behind the
// complete artifact producer matrix, tied to the image builder's same planner.
func snapshotContextPlanner(workflow lineageWorkflow, jobName string) string {
	job := workflow.jobs[jobName]
	steps := mappingChild(job, "steps")
	if steps == nil {
		return ""
	}
	gathers := false
	for _, step := range steps.Content {
		for _, line := range strings.Split(scalarChild(step, "run"), "\n") {
			gathers = gathers || strings.HasPrefix(strings.TrimSpace(line), "forge-ci container snapshot-images gather-context ")
		}
	}
	if gathers {
		for _, need := range needsOf(job) {
			planner := snapshotMatrixPlanner(workflow, need, "artifact", "artifacts")
			if planner != "" && slices.Contains(needsOf(job), planner) {
				return planner
			}
		}
	}
	return ""
}

func TestSnapshotLineageProofsFailClosedOnNamespaceMatrixAndNeedsChanges(t *testing.T) {
	t.Parallel()
	body := string(reporoot.ReadFile(t, ".github/workflows/snapshot-images.yml"))
	for _, tc := range []struct{ name, old, replacement string }{
		{"valid", "", ""},
		{"other namespace", "name: ${{ inputs['artifact-name'] }}-inputs", "name: ${{ inputs['other-name'] }}-inputs"},
		{"missing build barrier", "needs: [plan, build]", "needs: [plan]"},
		{"missing context dependency", "needs: [plan, prepare-context]", "needs: [plan]"},
		{"missing producer barrier", "needs: [plan, build-artifacts]", "needs: [plan]"},
		{"other context namespace", "name: ${{ inputs['artifact-name'] }}-context", "name: ${{ inputs['other-name'] }}-context"},
		{"other producer namespace", "name: ${{ inputs['artifact-name'] }}-dependency-${{ matrix.artifact.name }}", "name: ${{ inputs['other-name'] }}-dependency-${{ matrix.artifact.name }}"},
		{"unrelated artifact matrix", "artifact: ${{ fromJson(needs.plan.outputs.artifacts) }}", "artifact: ${{ fromJson(inputs.artifacts) }}"},
		{"untyped gather", "forge-ci container snapshot-images gather-context ", "forge-ci arbitrary-gather "},
		{"unrelated matrix", "image: ${{ fromJson(needs.plan.outputs.images) }}", "image: ${{ fromJson(inputs.images) }}"},
		{"untyped planner", "forge-ci container snapshot-images plan ", "forge-ci arbitrary-plan "},
		{"mismatched emitter", "images: ${{ steps.plan.outputs.images }}", "images: ${{ steps.other.outputs.images }}"},
		{"broad pattern", "-layout-${{ matrix.image.name }}-*", "-*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := body
			if tc.old != "" {
				require.Contains(t, body, tc.old)
				mutated = strings.Replace(body, tc.old, tc.replacement, 1)
			}
			doc, err := effectiveWorkflow([]byte(mutated))
			require.NoError(t, err)
			workflow := lineageWorkflow{name: "snapshot.yml", jobs: map[string]*yaml.Node{}}
			jobs := mappingChild(doc, "jobs")
			for i := 0; i < len(jobs.Content); i += 2 {
				workflow.jobs[jobs.Content[i].Value] = jobs.Content[i+1]
			}
			findings, count := lineageFindings(map[string]lineageWorkflow{workflow.name: workflow})
			if tc.name == "valid" {
				require.Empty(t, findings)
				require.Equal(t, 9, count)
			} else {
				require.NotEmpty(t, findings, "an unsupported relationship must remain a lineage finding")
			}
		})
	}
}
