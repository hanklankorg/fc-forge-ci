// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// exampleCall matches an example job that calls one of this repository's
// reusable workflows, and captures the workflow's file name.
var exampleCall = regexp.MustCompile(`^diggsweden/forge-ci/\.github/workflows/([A-Za-z0-9._-]+\.yml)@`)

type workflowCallContract struct {
	On struct {
		WorkflowCall struct {
			Inputs map[string]struct {
				Required bool `yaml:"required"`
			} `yaml:"inputs"`
			Secrets map[string]any `yaml:"secrets"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
}

// The examples are what adopters copy, and actionlint cannot check the
// inputs of a reusable workflow it fetches remotely. Every GitHub example call
// of this repository's workflows passes only inputs and secrets the called
// workflow declares, and every input it requires, so a renamed or removed
// input breaks this test instead of the first adopter's release.
func TestExampleCallsMatchTheCalledWorkflows(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	contracts := map[string]workflowCallContract{}
	calls := 0

	err := filepath.WalkDir(filepath.Join(root, "examples"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || filepath.Ext(path) != ".yml" || strings.Contains(path, string(filepath.Separator)+"forgejo"+string(filepath.Separator)) {
			return walkErr
		}

		body, err := os.ReadFile(path) //nolint:gosec // repository example fixture.
		require.NoError(t, err)

		var example struct {
			Jobs map[string]struct {
				Uses    string         `yaml:"uses"`
				With    map[string]any `yaml:"with"`
				Secrets any            `yaml:"secrets"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(body, &example), path)

		for jobName, job := range example.Jobs {
			match := exampleCall.FindStringSubmatch(job.Uses)
			if match == nil {
				continue
			}

			calls++

			contract, known := contracts[match[1]]
			if !known {
				called, readErr := os.ReadFile(filepath.Join(root, ".github", "workflows", match[1])) //nolint:gosec // repository workflow.
				require.NoError(t, readErr, "%s job %q calls a workflow this repository does not have", path, jobName)
				require.NoError(t, yaml.Unmarshal(called, &contract), match[1])
				contracts[match[1]] = contract
			}

			checkExampleCall(t, path+" job "+jobName, match[1], contract, job.With, job.Secrets)
		}

		return nil
	})
	require.NoError(t, err)
	require.Positive(t, calls, "no example calls a reusable workflow; the guard has drifted")
}

func checkExampleCall(t *testing.T, where, workflow string, contract workflowCallContract, with map[string]any, secrets any) {
	t.Helper()

	declared := contract.On.WorkflowCall

	for input := range with {
		if _, ok := declared.Inputs[input]; !ok {
			t.Errorf("%s passes %s an input it does not declare: %s", where, workflow, input)
		}
	}

	for input, spec := range declared.Inputs {
		if _, passed := with[input]; spec.Required && !passed {
			t.Errorf("%s leaves out %s's required input %s", where, workflow, input)
		}
	}

	// `secrets: inherit` is a string and names nothing to check.
	mapping, isMap := secrets.(map[string]any)
	if !isMap {
		return
	}

	for secret := range mapping {
		if _, ok := declared.Secrets[secret]; !ok {
			t.Errorf("%s passes %s a secret it does not declare: %s", where, workflow, secret)
		}
	}
}
