// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package policy

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/ghaenv"
	"github.com/diggsweden/forge-ci/internal/testutil/glabenv"
)

// On a pull or merge request, and only then, the gate adds --baseline-ref
// origin/<target branch> to the nanolinter command it wraps, which is how
// GitHub's lint-nanolinter.yml and GitLab's nanolinter component call it. A
// push reports everything, whatever base variable its environment carries.
func TestNanolinterJudgesAPullRequestAgainstItsTargetBranch(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T) func(key, value string)
		env   map[string]string
		want  string
	}{
		{
			"GitHub pull request", githubEnv,
			map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_BASE_REF": "main", "GITHUB_REF": "refs/pull/7/merge"},
			"verify --report sarif --out - --baseline-ref origin/main\n",
		},
		{
			"GitHub push", githubEnv,
			map[string]string{"GITHUB_EVENT_NAME": "push", "GITHUB_BASE_REF": "main", "GITHUB_REF": "refs/heads/main", "GITHUB_REF_TYPE": "branch"},
			"verify --report sarif --out -\n",
		},
		{
			"GitLab merge request", gitlabEnv,
			map[string]string{"CI_PIPELINE_SOURCE": "merge_request_event", "CI_MERGE_REQUEST_TARGET_BRANCH_NAME": "release/1.x"},
			"verify --report sarif --out - --baseline-ref origin/release/1.x\n",
		},
		{"GitLab push", gitlabEnv, map[string]string{"CI_PIPELINE_SOURCE": "push"}, "verify --report sarif --out -\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setenv := test.setup(t)
			for key, value := range test.env {
				setenv(key, value)
			}

			dir := t.TempDir()
			log := filepath.Join(dir, "argv")
			tool := filepath.Join(dir, "nanolinter")

			script := "#!/bin/sh\necho \"$*\" > '" + log + "'\n" +
				`echo '{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"nanolinter"}},"results":[]}]}'` + "\n"
			if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}

			command := nanolinterCmd()
			command.Writer, command.ErrWriter = io.Discard, io.Discard

			if err := command.Run(t.Context(), []string{"nanolinter", "--", tool, "verify", "--report", "sarif", "--out", "-"}); err != nil {
				t.Fatal(err)
			}

			got, err := os.ReadFile(log)
			if err != nil || string(got) != test.want {
				t.Fatalf("nanolinter arguments = %q (%v), want %q", got, err, test.want)
			}
		})
	}
}

func githubEnv(t *testing.T) func(key, value string) {
	t.Helper()

	return ghaenv.Setup(t).Setenv
}

func gitlabEnv(t *testing.T) func(key, value string) {
	t.Helper()

	return glabenv.Setup(t).Setenv
}

func TestNanolinterTypedModeRefusesAmbiguousOrMalformedInputs(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--offline=yes"},
		{"--offline=1"},
		{"--offline="},
		{"--required-lints=secrets", "--", "must-not-execute"},
		{"--disable-checks=osv", "--", "must-not-execute"},
	} {
		command := nanolinterCmd()
		command.Writer, command.ErrWriter = io.Discard, io.Discard

		err := command.Run(t.Context(), append([]string{"nanolinter"}, args...))
		if !errors.Is(err, errs.ErrUsage) {
			t.Fatalf("args %q: got %v, want usage refusal before command execution", args, err)
		}
	}

	// No arguments retains the existing explicit-child-command contract.
	err := nanolinterCmd().Run(t.Context(), []string{"nanolinter"})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("legacy no-command invocation = %v, want usage error", err)
	}
}
