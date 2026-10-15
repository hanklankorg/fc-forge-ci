// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"strings"
	"testing"
)

// The prepare job pushes the release commit to the release branch with the
// bot's key, so a request is admitted only at the head of that branch. The
// step, run against the CLI of this tree in a repository as the job's checkout
// leaves it, refuses a request that is ahead of the branch (its commits would
// reach it unreviewed), one on another line of history, and one the branch has
// moved past. It runs after the tag guard and before the action that loads the
// signing key. The branch is a validated input, not main by assumption.
func TestPrepareReleaseRequiresTheRequestAtTheHeadOfTheReleaseBranch(t *testing.T) {
	t.Parallel()

	const (
		request = "release-request/v1.2.3"
		name    = "Require the release request at the head of the release branch"
	)

	var workflow prepareReleaseWorkflow
	loadYAML(t, ".forgejo/workflows/prepare-release.yml", &workflow)

	order := map[string]int{}
	for index, step := range workflow.Jobs["prepare"].Steps {
		order[step.Name+step.Uses] = index
	}

	guard, gate, prepare := order["Reject non-stable release request./.forge-ci-helpers/.forgejo/actions/guard-release-tag"],
		order[name], order["Sign changelog commit and create final tag./.forge-ci-helpers/.forgejo/actions/prepare-release"]
	if guard == 0 || gate <= guard || prepare <= gate {
		t.Fatalf("step order: tag guard %d, branch gate %d, prepare action %d", guard, gate, prepare)
	}

	selected := workflowStep(t, "prepare-release.yml", "prepare", name)
	if selected.Env["RELEASE_REQUEST"] != "${{ forgejo.ref_name }}" ||
		selected.Env["TARGET_BRANCH"] != "${{ needs.coordinates.outputs.release_branch }}" || len(selected.Env) != 2 {
		t.Fatalf("the branch gate reads %q, want only the event's ref name and the validated release branch", selected.Env)
	}

	for _, test := range []struct {
		name string
		// arrange leaves the request tag and origin/stable in the repository,
		// whose one commit so far is on that branch.
		arrange  []string
		accepted bool
		message  string
	}{
		{"at the head of the branch", []string{"tag " + request, "update-ref refs/remotes/origin/stable HEAD"}, true, ""},
		{
			"ahead of the branch",
			[]string{"update-ref refs/remotes/origin/stable HEAD", "commit --quiet --allow-empty -m unreviewed", "tag " + request},
			false, "AHEAD of branch HEAD",
		},
		{
			"the branch has moved past it",
			[]string{"tag " + request, "commit --quiet --allow-empty -m later", "update-ref refs/remotes/origin/stable HEAD"},
			false, "tag commit is not branch HEAD",
		},
		{
			"on another line of history",
			[]string{
				"commit --quiet --allow-empty -m main", "update-ref refs/remotes/origin/stable HEAD",
				"checkout --quiet --detach HEAD~1", "commit --quiet --allow-empty -m side", "tag " + request,
			},
			false, "not in the history of branch 'stable'",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			box, env := engineSandbox(t, "prepare-release", nil)
			// The job reads the resolver from its helper checkout.
			box.write(".forge-ci-helpers/.forgejo/bin/lib-forge-ci.sh", readSandboxFile(t, box, ".forgejo/bin/lib-forge-ci.sh"), 0o600)

			for _, arguments := range append([]string{"init --quiet --initial-branch stable", "commit --quiet --allow-empty -m first"}, test.arrange...) {
				arguments = "-c user.name=fixture -c user.email=fixture@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false " + arguments
				if out, err := git(t, box.root, strings.Fields(arguments)...); err != nil {
					t.Fatalf("git %s: %v\n%s", arguments, err, out)
				}
			}

			got := box.run(selected.Run, append(env, "RELEASE_REQUEST="+request, "TARGET_BRANCH=stable", "RELEASE_BRANCH=main", "BRANCH=main")...)
			if (got.status == 0) != test.accepted || !strings.Contains(got.stdout+got.stderr, test.message) {
				t.Fatalf("status %d, accepted want %t, message want %q\nstdout %s\nstderr %s",
					got.status, test.accepted, test.message, got.stdout, got.stderr)
			}
		})
	}
}

// The release branch is a caller input, so the coordinates job admits it as a
// branch name before any job reads it.
func TestPrepareReleaseValidatesTheReleaseBranch(t *testing.T) {
	t.Parallel()

	selected := workflowStep(t, "prepare-release.yml", "coordinates", "Validate release branch")

	for _, test := range []struct {
		branch   string
		accepted bool
	}{
		{"main", true},
		{"release/1.x", true},
		{"", false},
		{"bad..name", false},
		{"-main", false},
	} {
		t.Run(test.branch, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "prepare-release")
			output := box.write("output", "", 0o600)

			got := box.run(selected.Run, "RELEASE_BRANCH_INPUT="+test.branch, "FORGEJO_OUTPUT="+output)
			if (got.status == 0) != test.accepted {
				t.Fatalf("branch %q: status %d, accepted want %t\nstderr %s", test.branch, got.status, test.accepted, got.stderr)
			}

			if test.accepted && readSandboxFile(t, box, "output") != "release_branch="+test.branch+"\n" {
				t.Fatalf("branch %q: output %q", test.branch, readSandboxFile(t, box, "output"))
			}
		})
	}
}
