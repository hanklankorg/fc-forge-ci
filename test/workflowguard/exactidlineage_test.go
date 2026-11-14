// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var exactIDNeeds = regexp.MustCompile(`^\$\{\{ needs\.([A-Za-z0-9_-]+)\.outputs\.([A-Za-z0-9_-]+) \}\}$`)
var originalUploadID = regexp.MustCompile(`^\$\{\{ steps\.([A-Za-z0-9_-]+)\.outputs\.artifact-id \}\}$`)

// Lineage only: a SINGLE original pinned native upload output, passed directly
// through the producer job's outputs, in this run/repository. Runtime attempt,
// metadata, source and hash admission remain the receiving leaf's obligation.
func exactIDLineageProofs(workflow lineageWorkflow, jobName string) map[string]bool {
	proofs := map[string]bool{}
	job := workflow.jobs[jobName]

	steps := mappingChild(job, "steps")
	if steps == nil {
		return proofs
	}

	for _, step := range steps.Content {
		if scalarChild(step, "uses") != "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c" {
			continue
		}

		with := mappingChild(step, "with")
		ids := scalarChild(with, "artifact-ids")

		match := exactIDNeeds.FindStringSubmatch(ids)
		if match == nil || !slices.Contains(needsOf(job), match[1]) {
			continue
		}

		foreign := false

		for _, key := range []string{"name", "pattern", "run-id", "repository", "github-token"} {
			if mappingChild(with, key) != nil {
				foreign = true
			}
		}

		if foreign {
			continue
		}

		producer := workflow.jobs[match[1]]

		output := originalUploadID.FindStringSubmatch(scalarChild(mappingChild(producer, "outputs"), match[2]))
		if output == nil {
			continue
		}

		uploads := mappingChild(producer, "steps")
		if uploads == nil {
			continue
		}

		for _, upload := range uploads.Content {
			overwrite := scalarChild(mappingChild(upload, "with"), "overwrite")
			if scalarChild(upload, "id") == output[1] && scalarChild(upload, "uses") == "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" && (overwrite == "" || overwrite == "false") {
				proofs[fmt.Sprintf("artifact IDs %q", ids)] = true
			}
		}
	}

	return proofs
}

func TestExactIDLineageRequiresOriginalUpload(t *testing.T) {
	const fixture = `jobs:
  producer:
    outputs:
      artifact-id: ${{ steps.upload.outputs.artifact-id }}
    steps:
      - id: upload
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a
        with:
          overwrite: false
  receiver:
    needs: producer
    steps:
      - uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c
        with:
          artifact-ids: ${{ needs.producer.outputs.artifact-id }}
`

	for _, test := range []struct{ name, from, to string }{
		{name: "original"},
		{name: "missing-needs", from: "needs: producer", to: "needs: other"},
		{name: "relabelled-output", from: "steps.upload.outputs.artifact-id", to: "steps.wrapper.outputs.artifact-id"},
		{name: "multiple-ids", from: "artifact-ids: ${{ needs.producer.outputs.artifact-id }}", to: "artifact-ids: 123,456"},
		{name: "cross-run", from: "artifact-ids:", to: "run-id: 999\n          artifact-ids:"},
		{name: "name-fallback", from: "artifact-ids:", to: "name: fallback\n          artifact-ids:"},
		{name: "pattern", from: "artifact-ids:", to: "pattern: '*'\n          artifact-ids:"},
		{name: "mutable-upload", from: "upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", to: "upload-artifact@main"},
		{name: "overwrite", from: "overwrite: false", to: "overwrite: true"},
		{name: "dynamic-overwrite", from: "overwrite: false", to: "overwrite: ${{ inputs.overwrite }}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := fixture
			if test.from != "" {
				text = strings.Replace(text, test.from, test.to, 1)
			}

			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
				t.Fatal(err)
			}

			jobs := mappingChild(doc.Content[0], "jobs")
			workflow := lineageWorkflow{jobs: map[string]*yaml.Node{"producer": mappingChild(jobs, "producer"), "receiver": mappingChild(jobs, "receiver")}}

			proof := exactIDLineageProofs(workflow, "receiver")
			if got, want := len(proof) > 0, test.name == "original"; got != want {
				t.Fatalf("proof=%v, want %v", proof, want)
			}
		})
	}
}
