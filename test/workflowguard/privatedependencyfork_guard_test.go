// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/validate"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// privateDependencyForkCondition is when a build that reads private
// dependencies must refuse: the caller enabled them, the trigger is of the
// pull-request family, and the head is another repository, a fork.
const privateDependencyForkCondition = "${{ inputs['private-dependencies'] && startsWith(github.event_name, 'pull_request') && " +
	"github.event.pull_request.head.repo.full_name != github.repository }}"

// privateDependencyBuilds are the build workflows that hand the run token to
// a package manager as DEPENDENCY_TOKEN.
var privateDependencyBuilds = []string{"build-npm.yml", "build-maven.yml", "build-gradle-app.yml"}

// privateDependencyForkGaps judges a build workflow that offers private
// dependencies: every job with a step that receives DEPENDENCY_TOKEN must
// start with the fork refusal, which runs the event-context gate under
// exactly privateDependencyForkCondition, receives nothing itself and cannot
// be survived. Being the job's first step puts it before the checkout and
// before every step that holds the run token.
func privateDependencyForkGaps(body []byte) []string {
	var workflow eventWorkflow
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		return []string{err.Error()}
	}

	var (
		gaps    []string
		guarded int
	)

	for name, job := range workflow.Jobs {
		credentialed := false

		for _, step := range job.Steps {
			for i := 0; i+1 < len(step.Env.Content); i += 2 {
				credentialed = credentialed || step.Env.Content[i].Value == "DEPENDENCY_TOKEN"
			}
		}

		if !credentialed {
			continue
		}

		guarded++

		first := job.Steps[0]

		switch {
		case strings.TrimSpace(first.Run) != "forge-ci validate event-context":
			gaps = append(gaps, fmt.Sprintf("job %s does not start with the fork refusal (first step %q)", name, first.Name))
		case first.If != privateDependencyForkCondition:
			gaps = append(gaps, fmt.Sprintf("job %s refuses forks under %q", name, first.If))
		case first.Continue.Kind != 0 || job.Continue.Kind != 0:
			gaps = append(gaps, fmt.Sprintf("job %s can continue past the fork refusal", name))
		case first.Env.Kind != 0 || first.With.Kind != 0 || first.Uses != "" || (first.Shell != "" && first.Shell != "bash"):
			gaps = append(gaps, fmt.Sprintf("job %s gives the fork refusal an environment, an action or another shell", name))
		}
	}

	if guarded == 0 {
		gaps = append(gaps, "no job hands DEPENDENCY_TOKEN to a step")
	}

	return gaps
}

// Fork pull requests are refused before checkout when private dependencies
// are enabled (docs/private-dependencies.md). The decision is a workflow
// expression GitHub evaluates, so this pins its two halves: the expression
// and its place in each build workflow, and the gate it runs, which refuses
// every pull-request event.
func TestPrivateDependencyBuildsRefuseForkPullRequestsFirst(t *testing.T) {
	t.Parallel()

	for _, name := range privateDependencyBuilds {
		body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}

		if gaps := privateDependencyForkGaps(body); len(gaps) != 0 {
			t.Errorf("%s: %s", name, strings.Join(gaps, "; "))
		}
	}

	for _, event := range []string{"pull_request", "pull_request_target"} {
		var refused *validate.EventContextError
		if err := validate.RequireAllowedEvent(event, nil); !errors.As(err, &refused) {
			t.Errorf("the event-context gate the refusal runs admits %s: %v", event, err)
		}
	}

	if err := validate.RequireAllowedEvent("", nil); !errors.Is(err, errs.ErrMissingInput) {
		t.Errorf("the event-context gate admits an unknown trigger: %v", err)
	}
}

func TestPrivateDependencyForkGapsCanFail(t *testing.T) {
	t.Parallel()

	const refusal = `      - name: Refuse private dependency access from fork pull requests
        if: ` + privateDependencyForkCondition + `
        run: forge-ci validate event-context
`

	const checkout = `      - name: Checkout repository
        env:
          GITHUB_TOKEN: ${{ github.token }}
        run: forge-ci platform checkout
`

	const install = `      - name: Install
        env:
          DEPENDENCY_TOKEN: ${{ inputs['private-dependencies'] && github.token || '' }}
        run: forge-ci build npm
`

	workflow := func(steps ...string) []byte {
		return []byte("jobs:\n  build:\n    runs-on: ubuntu-26.04\n    steps:\n" + strings.Join(steps, ""))
	}

	if gaps := privateDependencyForkGaps(workflow(refusal, checkout, install)); len(gaps) != 0 {
		t.Fatalf("the shipped shape is refused: %v", gaps)
	}

	for name, body := range map[string][]byte{
		"refusal after checkout": workflow(checkout, refusal, install),
		"refusal missing":        workflow(checkout, install),
		"same-repository only":   workflow(strings.Replace(refusal, " && github.event.pull_request.head.repo.full_name != github.repository", "", 1), checkout, install),
		"pull_request only":      workflow(strings.Replace(refusal, "startsWith(github.event_name, 'pull_request')", "github.event_name == 'pull_request'", 1), checkout, install),
		"survivable":             workflow(refusal+"        continue-on-error: true\n", checkout, install),
		"credentialed refusal":   workflow(refusal+"        env:\n          GITHUB_TOKEN: ${{ github.token }}\n", checkout, install),
		"softened command":       workflow(strings.Replace(refusal, "forge-ci validate event-context", "forge-ci validate event-context || true", 1), checkout, install),
		"no credentialed job":    workflow(refusal, checkout),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if gaps := privateDependencyForkGaps(body); len(gaps) == 0 {
				t.Fatal("a build that can reach the dependency credential from a fork is accepted")
			}
		})
	}
}
