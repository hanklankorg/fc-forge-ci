// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// needsGateViolations lists the jobs that leave the success of a need to the
// forge's default: each job that needs another must name
// needs.<job>.result == 'success' for every need in its own condition.
func needsGateViolations(data []byte) ([]string, error) {
	parsed, err := parseWorkflow(data)
	if err != nil {
		return nil, err
	}

	var violations []string

	for name, current := range parsed.jobs {
		condition := scalarString(mappingValue(current.node, "if"))

		for _, need := range jobNeeds(current) {
			if !strings.Contains(condition, fmt.Sprintf("needs.%s.result == 'success'", need)) {
				violations = append(violations, fmt.Sprintf("job %s does not gate on needs.%s.result == 'success'", name, need))
			}
		}
	}

	slices.Sort(violations)

	return violations, nil
}

// needsGate returns the condition that runs a job on the success of its needs
// alone, followed by any extra conditions.
func needsGate(needs []string, extra ...string) string {
	gates := make([]string, 0, len(needs)+len(extra))
	for _, need := range needs {
		gates = append(gates, fmt.Sprintf("needs.%s.result == 'success'", need))
	}

	return "${{ " + strings.Join(append(gates, extra...), " && ") + " }}"
}

// Forgejo 17 evaluates a job's own condition in a called workflow but not the
// implicit success() of its needs: after sign had failed it started the
// publication jobs of sign-and-publish-release.yml. Every layer file is a
// called workflow, so every job there that needs another states the success
// it relies on.
func TestLayerJobsGateOnTheirNeeds(t *testing.T) {
	t.Parallel()

	for _, path := range allWorkflows(t) {
		violations, err := needsGateViolations(reporoot.ReadFile(t, path))
		if err != nil {
			t.Errorf("%s: %v", path, err)

			continue
		}

		for _, violation := range violations {
			t.Errorf("%s: %s", path, violation)
		}
	}
}

func TestNeedsGateRejections(t *testing.T) {
	t.Parallel()

	const gated = "jobs:\n  a:\n    runs-on: x\n  b:\n    needs: [a]\n    if: ${{ needs.a.result == 'success' }}\n"
	if violations, err := needsGateViolations([]byte(gated)); err != nil || len(violations) != 0 {
		t.Errorf("a gated job was refused: %v %v", violations, err)
	}

	for name, document := range map[string]string{
		"no condition":            "jobs:\n  b:\n    needs: a\n",
		"condition on outputs":    "jobs:\n  b:\n    needs: a\n    if: ${{ needs.a.outputs.x != 'false' }}\n",
		"one of two needs gated":  "jobs:\n  c:\n    needs: [a, b]\n    if: ${{ needs.a.result == 'success' }}\n",
		"result other than pass":  "jobs:\n  b:\n    needs: [a]\n    if: ${{ needs.a.result != 'failure' }}\n",
		"status function instead": "jobs:\n  b:\n    needs: [a]\n    if: ${{ !failure() && !cancelled() }}\n",
	} {
		violations, err := needsGateViolations([]byte(document))
		if err != nil || len(violations) == 0 {
			t.Errorf("the guard accepted a job with %s (%v)", name, err)
		}
	}
}
