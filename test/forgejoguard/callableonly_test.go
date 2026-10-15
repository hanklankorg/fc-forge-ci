// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// layerWorkflows lists the workflows a Forgejo instance discovers in this
// repository, as repository-relative paths.
func layerWorkflows(t *testing.T) []string {
	t.Helper()

	var paths []string

	for _, entry := range reporoot.ReadDir(t, ".forgejo/workflows") {
		if extension := filepath.Ext(entry.Name()); extension != ".yml" && extension != ".yaml" {
			t.Errorf(".forgejo/workflows/%s is not a workflow file; Forgejo discovers this directory", entry.Name())

			continue
		}

		paths = append(paths, ".forgejo/workflows/"+entry.Name())
	}

	if len(paths) == 0 {
		t.Fatal("found no Forgejo workflows to inspect")
	}

	return paths
}

// callableOnlyViolation reports why a workflow could start without a caller.
func callableOnlyViolation(data []byte) string {
	root, err := parseDocument(data)
	if err != nil {
		return err.Error()
	}

	events := mappingEntries(mappingValue(root, "on"))
	if len(events) != 1 || scalarString(events[0].Key) != "workflow_call" {
		return "`on:` must be a mapping with workflow_call as its only event"
	}

	return ""
}

// A Forgejo instance that hosts this repository runs whatever it discovers
// under .forgejo/workflows, and prefers that directory over .github/workflows.
// Every file there is a reusable workflow for consumers: it starts only when
// another workflow calls it, so a push, a pull request or a schedule in this
// repository starts none of them, and none can reach a signing secret of the
// hosting organization. The repository's own CI on Forgejo is a later step and
// will need its event stated here on purpose.
func TestLayerWorkflowsStartOnlyWhenCalled(t *testing.T) {
	t.Parallel()

	for _, path := range layerWorkflows(t) {
		if violation := callableOnlyViolation(reporoot.ReadFile(t, path)); violation != "" {
			t.Errorf("%s: %s", path, violation)
		}
	}
}

func TestCallableOnlyRejectsEveryOtherTrigger(t *testing.T) {
	t.Parallel()

	if violation := callableOnlyViolation([]byte("on:\n  workflow_call:\n    inputs: {}\njobs: {}\n")); violation != "" {
		t.Errorf("a workflow_call-only workflow was refused: %s", violation)
	}

	for name, document := range map[string]string{
		"push beside the call": "on:\n  workflow_call:\n  push:\njobs: {}\n",
		"push only":            "on:\n  push:\njobs: {}\n",
		"scalar event":         "on: push\njobs: {}\n",
		"event list":           "on: [workflow_call, push]\njobs: {}\n",
		"manual dispatch":      "on:\n  workflow_dispatch:\njobs: {}\n",
		"no trigger":           "jobs: {}\n",
	} {
		if callableOnlyViolation([]byte(document)) == "" {
			t.Errorf("the guard accepted a workflow with %s", name)
		}
	}
}
