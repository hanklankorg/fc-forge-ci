// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestForgejoSnapshotPublicOutputDiagnostic(t *testing.T) {
	RegisterLiveScenario(t)
	for _, mode := range []string{"artifact-first", "container-first"} {
		t.Run(mode, func(t *testing.T) { runForgejoSnapshotOutputDiagnostic(t, mode == "artifact-first") })
	}
}

func runForgejoSnapshotOutputDiagnostic(t *testing.T, withArtifacts bool) {
	t.Helper()

	target := Accept(t, provider.ForgeForgejo)
	files := snapshotCandidateFiles(t)
	var workflow map[string]any
	if err := yaml.Unmarshal([]byte(files[".forgejo/workflows/snapshot-images.yml"].Body), &workflow); err != nil {
		t.Fatal(err)
	}
	jobs := workflow["jobs"].(map[string]any)
	plan := jobs["plan"].(map[string]any)
	steps := plan["steps"].([]any)
	for _, raw := range steps {
		step := raw.(map[string]any)
		if step["id"] == "plan" {
			step["run"] = step["run"].(string) + `
printf 'DIAG_SNAPSHOT_PUBLISH=%s\n' "${SNAPSHOT_PUBLISH}"
cat snapshot-plan.json
`
		}
	}
	steps = append(steps, map[string]any{"name": "Observe public same-job step outputs", "if": "${{ always() }}", "env": map[string]any{
		"DIAG_STEP_OUTPUTS_JSON":  "${{ toJSON(steps.plan.outputs) }}",
		"DIAG_STEP_ENABLED":       "${{ steps.plan.outputs.enabled }}",
		"DIAG_STEP_HAS_ARTIFACTS": "${{ steps.plan.outputs['has-artifacts'] }}",
	}, "run": "for key in DIAG_STEP_OUTPUTS_JSON DIAG_STEP_ENABLED DIAG_STEP_HAS_ARTIFACTS; do printf '%s=%s\\n' \"$key\" \"${!key}\"; done"})
	plan["steps"] = steps
	producer := jobs["produce"].(map[string]any)
	delete(producer, "container")
	producer["steps"] = []any{map[string]any{"run": "printf 'DIAG_PRODUCER_NOOP=true\\n'"}}
	workflow["jobs"] = map[string]any{"coordinates": jobs["coordinates"], "plan": plan,
		"produce": producer,
		"probe":   map[string]any{"needs": []string{"coordinates", "plan", "produce"}, "if": jobs["context"].(map[string]any)["if"], "runs-on": "${{ fromJSON(inputs['runner-label']) }}", "steps": []any{map[string]any{"run": "printf 'DIAG_CONTEXT_CONDITION=true\\n'"}}},
		"diagnose": map[string]any{
			"needs": []string{"coordinates", "plan", "produce", "probe"}, "if": "${{ always() }}", "runs-on": "${{ fromJSON(inputs['runner-label']) }}",
			"steps": []any{map[string]any{"name": "Observe public downstream inputs and outputs", "env": map[string]any{
				"DIAG_NEEDS_JSON":      "${{ toJSON(needs.plan.outputs) }}",
				"DIAG_PLAN_RESULT":     "${{ needs.plan.result }}",
				"DIAG_ENABLED":         "${{ needs.plan.outputs.enabled }}",
				"DIAG_HAS_ARTIFACTS":   "${{ needs.plan.outputs['has-artifacts'] }}",
				"DIAG_CONTEXT_JSON":    "${{ toJSON(inputs['context-artifact']) }}",
				"DIAG_CONTEXT_TEXT":    "${{ inputs['context-artifact'] }}",
				"DIAG_PUBLISH_JSON":    "${{ toJSON(inputs['publish-containers']) }}",
				"DIAG_HAS_TEST":        "${{ needs.plan.outputs['has-artifacts'] == 'true' }}",
				"DIAG_EMPTY_TEST":      "${{ inputs['context-artifact'] == '' }}",
				"DIAG_COMBINED_TEST":   "${{ needs.plan.outputs['has-artifacts'] == 'true' && inputs['context-artifact'] == '' }}",
				"DIAG_TRUTHINESS_TEST": "${{ !inputs['context-artifact'] }}",
				"DIAG_PRODUCER_RESULT": "${{ needs.produce.result }}",
				"DIAG_CONTEXT_RESULT":  "${{ needs.probe.result }}",
			}, "run": "for key in DIAG_NEEDS_JSON DIAG_PLAN_RESULT DIAG_ENABLED DIAG_HAS_ARTIFACTS DIAG_CONTEXT_JSON DIAG_CONTEXT_TEXT DIAG_PUBLISH_JSON DIAG_HAS_TEST DIAG_EMPTY_TEST DIAG_COMBINED_TEST DIAG_TRUTHINESS_TEST DIAG_PRODUCER_RESULT DIAG_CONTEXT_RESULT; do printf '%s=%s\\n' \"$key\" \"${!key}\"; done"}},
		}}
	// Exercise the production dependency conditions without executing builds or
	// publication. Only this diagnostic scratch copy replaces producer work.
	workflow["on"].(map[string]any)["workflow_call"].(map[string]any)["outputs"] = map[string]any{}
	body, err := yaml.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}

	files[".forgejo/workflows/snapshot-images.yml"] = filePayload{Body: string(body), Mode: 0644}
	engine := NewBareScratchRepo(t, target, "snapshot-diagnostic-engine", "sha1", false)
	consumer := NewBareScratchRepo(t, target, "snapshot-diagnostic-source", "sha1", false)
	engineSHA, _, _ := nativeSnapshotTree(t, target, engine, files)
	nativeSnapshotPublishEngine(t, target, engine)
	config := ".forge-ci/artifacts.yml"
	if !withArtifacts {
		config = ".forge-ci/container-only.yml"
	}
	caller := fmt.Sprintf(`name: Snapshot output diagnostic
on:
  workflow_dispatch:
permissions:
  contents: read
jobs:
  diagnostic:
    uses: %s/.forgejo/workflows/snapshot-images.yml@%s
    with:
      branch: feature/artifact-source
      artifacts-config: %s
      forge-ci-repository: %s
      forge-ci-sha: %s
      runner-label: '["%s"]'
      artifact-job-image-go: %s
      image-repository: %s/never-published
      registry-user: unused
`, RepoSlug(target, engine), engineSHA, config, RepoSlug(target, engine), engineSHA, RunnerLabel(t, target), snapshotNativeToolsImage, strings.TrimPrefix(target.BaseURL(), "https://"))
	sourceFiles := snapshotCanaryFiles(false)
	if !withArtifacts {
		sourceFiles[config] = filePayload{Body: "containers:\n  - name: canary\n    container-file: Containerfile\n    context: .\n    platforms: linux/amd64\n    enable-scan: false\n    enable-slsa: false\n", Mode: 0644}
	}
	sourceFiles[".forgejo/workflows/snapshot-native.yml"] = filePayload{Body: caller, Mode: 0644}
	policy, dir, git := nativeSnapshotTree(t, target, consumer, sourceFiles)
	selected := nativeSnapshotSelected(t, target, consumer, dir, git)
	EnableActions(t, target, consumer)
	ctx, cancel := context.WithTimeout(Context(t), time.Minute)
	defer cancel()
	if err := discard(ctx, target, http.MethodPost, actionsURL(target, consumer, "/actions/workflows/snapshot-native.yml/dispatches"), map[string]string{"ref": "main"}, http.StatusNoContent); err != nil {
		t.Fatal(err)
	}

	run := waitSnapshotDispatch(t, target, consumer, policy)
	t.Logf("nonpublishing diagnostic: run=%d status=%s engine=%s policy=%s selected=%s", run.ID, run.Status, engineSHA, policy, selected)
	for _, artifact := range RunArtifacts(t, target, consumer, run.ID) {
		archive, err := fetch(Context(t), target, actionsURL(target, consumer, fmt.Sprintf("/actions/artifacts/%d/zip", artifact.ID)), maxArtifactZIP)
		if err != nil {
			t.Errorf("diagnostic artifact unavailable: id=%d", artifact.ID)
			continue
		}
		saveSnapshotEvidence(t, fmt.Sprintf("forgejo-diagnostic-artifact-%d.zip", artifact.ID), archive)
		t.Logf("diagnostic artifact retained: id=%d name=%s", artifact.ID, artifact.Name)
	}
	contextRan := false
	for _, job := range RunJobs(t, target, consumer, run.ID) {
		contextRan = contextRan || (job.Name == "probe" && job.Status == "success")
		if len(job.RunsOn) == 0 || job.Attempt == 0 || job.Status == "skipped" {
			t.Logf("non-runner wrapper: id=%d name=%s status=%s attempt=%d", job.ID, job.Name, job.Status, job.Attempt)
			continue
		}
		log, err := jobLog(t, target, consumer, job)
		if err != nil {
			t.Errorf("diagnostic runner log unavailable: id=%d name=%s", job.ID, job.Name)
			continue
		}
		log = redactTargetSecrets(target, log)
		saveSnapshotEvidence(t, fmt.Sprintf("forgejo-diagnostic-job-%d.log", job.ID), []byte(log))
		t.Logf("diagnostic job: id=%d name=%s status=%s", job.ID, job.Name, job.Status)
		if job.Status == "failure" {
			t.Logf("diagnostic failure:\n%s", readFailureExcerpt(strings.NewReader(log), 40))
		}
		for line := range strings.SplitSeq(log, "\n") {
			if strings.Contains(line, "DIAG_") || strings.Contains(line, `"enabled"`) {
				t.Log(line)
			}
		}
	}
	if run.Status != "success" {
		t.Fatal("diagnostic transport failed; inspect retained traces")
	}
	if !contextRan {
		t.Fatal("the production context condition did not admit the settled producer matrix")
	}
}
