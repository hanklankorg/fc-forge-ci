// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Pin these small scheduling conditions directly rather than interpreting the
// expression language. Skipped producers are legitimate only for reuse/empty
// plans; failures must not release the context or publication barriers.
func snapshotBarrierViolations(jobs *yaml.Node) []string {
	var failures []string
	for _, boundary := range []struct {
		job, condition string
		needs          []string
	}{
		{"build-artifacts", "${{ !inputs['use-build-artifacts'] && needs.plan.outputs.artifacts != '[]' }}", []string{"resolve-bootstrap", "plan"}},
		{"prepare-context", "${{ !cancelled() && needs.plan.result == 'success' && (needs.build-artifacts.result == 'success' || needs.build-artifacts.result == 'skipped') }}", []string{"plan", "build-artifacts"}},
		{"build", "${{ !failure() && !cancelled() && needs.prepare-context.result == 'success' && needs.plan.outputs.builds != '[]' }}", []string{"plan", "prepare-context"}},
		{"publish", "${{ !failure() && !cancelled() && needs.build.result == 'success' && needs.plan.outputs.images != '[]' }}", []string{"plan", "build"}},
		{"collect", "${{ !cancelled() && needs.plan.result == 'success' && needs.prepare-context.result == 'success' && ((needs.build.result == 'success' && needs.publish.result == 'success') || (needs.plan.outputs.images == '[]' && needs.build.result == 'skipped' && needs.publish.result == 'skipped')) }}", []string{"plan", "prepare-context", "build", "publish"}},
	} {
		job := mappingChild(jobs, boundary.job)
		if scalarChild(job, "if") != boundary.condition {
			failures = append(failures, boundary.job+": failure/skip condition changed")
		}
		got := needsOf(job)
		for _, need := range boundary.needs {
			found := false
			for _, dependency := range got {
				found = found || dependency == need
			}
			if !found {
				failures = append(failures, boundary.job+": missing complete dependency "+need)
			}
		}
	}
	return failures
}

func TestSnapshotProducerBarriersRejectFailureBypasses(t *testing.T) {
	t.Parallel()
	body := string(reporoot.ReadFile(t, ".github/workflows/snapshot-images.yml"))
	for _, tc := range []struct{ name, old, replacement string }{
		{"valid", "", ""},
		{"missing context need", "needs: [plan, prepare-context]", "needs: [plan]"},
		{"missing producer need", "needs: [plan, build-artifacts]", "needs: [plan]"},
		{"failed producer", "needs.build-artifacts.result == 'skipped'", "needs.build-artifacts.result == 'failure'"},
		{"failed context", "needs.prepare-context.result == 'success'", "needs.prepare-context.result != 'cancelled'"},
		{"failed image matrix", "needs.build.result == 'success'", "needs.build.result != 'cancelled'"},
		{"failed publication", "needs.publish.result == 'success'", "needs.publish.result != 'cancelled'"},
		{"empty escape without empty plan", "needs.plan.outputs.images == '[]' && ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := body
			if tc.old != "" {
				require.Contains(t, body, tc.old)
				mutated = strings.Replace(body, tc.old, tc.replacement, 1)
			}
			doc, err := effectiveWorkflow([]byte(mutated))
			require.NoError(t, err)
			failures := snapshotBarrierViolations(mappingChild(doc, "jobs"))
			if tc.name == "valid" {
				require.Empty(t, failures)
			} else {
				require.NotEmpty(t, failures)
			}
		})
	}
}
