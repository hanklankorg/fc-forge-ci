// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"regexp"
	"strings"
	"testing"
)

// rowValueDeref matches a matrix row value read as an object or a list:
// matrix.build.name, matrix.build['cache-tag'] or toJSON(matrix.archs).
var rowValueDeref = regexp.MustCompile(`matrix\.[A-Za-z0-9_-]+[.[]|matrix\['[^']*'\][.[]|toJSON\(matrix\.`)

// Forgejo appends a row's values to the name of a job whose matrix comes
// from another job's output, whatever name the job sets, and shows an object
// or a list there as a Go map or slice: "Build: x (map[arch:amd64 ...])". So
// the rows of such a matrix hold short scalar values only, and the job reads
// anything else by them from its planner's outputs. The job's own name holds
// no row value, which Forgejo would show twice.
func TestPlannedMatrixRowsHoldScalarValues(t *testing.T) {
	t.Parallel()

	planned := 0

	for _, relative := range allWorkflows(t) {
		for name, target := range loadWorkflow(t, relative).jobs {
			matrix := mappingValue(mappingValue(target.node, "strategy"), "matrix")
			if !anyString(matrix, func(value string) bool { return strings.Contains(value, "needs.") }) {
				continue
			}

			planned++

			if anyString(target.node, rowValueDeref.MatchString) {
				t.Errorf("%s: job %s reads a value of its planned matrix row as an object or a list, which Forgejo shows in the job's name", relative, name)
			}

			if jobName := scalarString(mappingValue(target.node, "name")); strings.Contains(jobName, "matrix") {
				t.Errorf("%s: job %s is named %q, and Forgejo appends its row's values to the name already", relative, name, jobName)
			}
		}
	}

	if planned == 0 {
		t.Fatal("found no matrix planned by another job to inspect")
	}
}
