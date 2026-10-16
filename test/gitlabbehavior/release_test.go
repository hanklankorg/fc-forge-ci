// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The real orchestration must reject misplaced credentials before it invokes
// even the trusted CLI. These cases never start a network tool or a signer.
func TestReleaseJobRejectsCredentialsOutsideItsRole(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{"Check: release request", "RELEASE_SSH_SIGNING_KEY"},
		{"Prepare: changelog", "RELEASE_COSIGN_PRIVATE_KEY"},
		{"Prepare: version", "RELEASE_SSH_SIGNING_KEY"},
		{"Build: release files", "RELEASE_TOKEN"},
		{"Sign: files and images", "RELEASE_SSH_SIGNING_KEY"},
		{"Sign: files and images", "RELEASE_TOKEN"},
		{"Publish: images", "RELEASE_COSIGN_PRIVATE_KEY"},
		{"Publish: release", "RELEASE_GPG_PRIVATE_KEY"},
		{"Publish: tidy up", "RELEASE_SSH_SIGNING_KEY"},
		// A job's former name has no role, so it holds no secret.
		{"release-sign", "RELEASE_COSIGN_PRIVATE_KEY"},
	} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
			command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-c", `source "$1"; rc_engine() { echo engine-called; }; rc_release_context`, "release-guard", script) //nolint:gosec // Reviewed script, isolated environment and an inert CLI function.
			command.Dir = t.TempDir()
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + command.Dir, "CI_JOB_NAME=" + pair[0], pair[1] + "=inert-canary-value"}

			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), pair[1]+" is available to the wrong release job") ||
				strings.Contains(string(output), "inert-canary-value") || strings.Contains(string(output), "engine-called") {
				t.Fatalf("misplaced credential was not refused cleanly: %v", err)
			}
		})
	}
}

func TestReleaseHandoffNeverSourcesItsDigestAsShell(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "digest"), []byte("$(touch exploited)\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
	command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-c", `set -eu; source "$1"; rc_engine() { echo engine-called; }; rc_read_handoff "$2"`, "handoff-guard", script, dir) //nolint:gosec // Reviewed script and inert CLI, reading only fixture data.
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}

	output, err := command.CombinedOutput()
	if err == nil || strings.Contains(string(output), "engine-called") {
		t.Fatal("invalid digest reached an engine operation")
	}

	if _, err := os.Stat(filepath.Join(dir, "exploited")); !os.IsNotExist(err) {
		t.Fatal("handoff data was evaluated as shell")
	}
}

func TestBaseSigningRequiresExplicitPolicyOptIn(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "base-images.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
	command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-eu", "-c", //nolint:gosec // Actual trusted shell with an inert engine and owned artifacts only.
		`source "$1"; rc_engine() { echo engine-called; }; rc_sign_bases`, "base-policy", script)
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "DIST_DIR=" + dir, "RC_BASE_IMAGES=false"}

	output, err := command.CombinedOutput()
	if err == nil || strings.Contains(string(output), "engine-called") {
		t.Fatal("a producer base ledger acquired authority without policy opt-in")
	}
}

func TestPublicationRegistryBelongsToTheSeparateProject(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		project, image string
		allowed        bool
	}{
		{"owner/publication", "registry.invalid/owner/publication", true},
		{"owner/publication", "registry.invalid/owner/publication/bases", true},
		{"owner/source", "registry.invalid/owner/source", false},
		{"owner/publication", "registry.invalid/owner/source/bases", false},
		{"owner/publication", "registry.invalid/owner/publication-other", false},
		{"owner/publication", "other.invalid/owner/publication", false},
	} {
		t.Run(tc.image, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
			command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-eu", "-c", `source "$1"; rc_publication_scope`, "publication-scope", script) //nolint:gosec // Actual scope guard, closed environment and no external commands.

			command.Env = []string{"PATH=/usr/bin:/bin", "CI_REGISTRY=registry.invalid", "CI_PROJECT_PATH=owner/source",
				"RC_PUBLICATION_PROJECT=" + tc.project, "RC_PUBLICATION_IMAGE_REPOSITORY=" + tc.image}

			if err := command.Run(); (err == nil) != tc.allowed {
				t.Fatalf("publication scope admitted=%t, want %t", err == nil, tc.allowed)
			}
		})
	}
}

// GitLab hands every project CI/CD variable to the job as an environment
// variable, and the engine's branch flags fall back to RELEASE_BRANCH and
// BRANCH. Only the head check used to name the branch, so a project variable
// could move where the release commit was pushed while the check stayed on
// main. Every branch-reading call now gets the one value rc_release_context
// set: the release-branch input, else the project's default branch.
func TestReleaseBranchIsNotTakenFromTheEnvironment(t *testing.T) {
	t.Parallel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")

	body, err := os.ReadFile(script) //nolint:gosec // repository contract fixture.
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.ReplaceAll(string(body), "\\\n", " ")
	for _, call := range []string{"validate tag commit", "version render-changelog", "version commit-changelog-release"} {
		for line := range strings.SplitSeq(joined, "\n") {
			if strings.Contains(line, "rc_engine "+call) && !strings.Contains(line, `--branch "${RC_RELEASE_BRANCH}"`) {
				t.Errorf("rc_engine %s does not pass the release branch explicitly: %s", call, strings.TrimSpace(line))
			}
		}
	}

	for _, test := range []struct{ name, input, want string }{
		{name: "default branch", input: "", want: "main"},
		{name: "input", input: "release/1.x", want: "release/1.x"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			output, runErr := runReleasePrepare(t, script, test.input)
			if runErr != nil {
				t.Fatalf("prepare did not run: %v\n%s", runErr, output)
			}

			if strings.Contains(output, "hostile") {
				t.Fatalf("an environment variable chose the release branch:\n%s", output)
			}

			for _, want := range []string{"git -c core.hooksPath=/dev/null checkout -b " + test.want, "--branch " + test.want + " --repository team/app"} {
				if !strings.Contains(output, want) {
					t.Errorf("prepare output lacks %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestReleaseBranchInputMustBeABranchName(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
	command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-c", //nolint:gosec // Reviewed script with an inert engine.
		`set -eu; source "$1"; rc_engine() { printf 'engine %s\n' "$*"; }; rc_release_context`, "branch-name", script)
	command.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "CI_JOB_NAME=Prepare: release commit and tag",
		"CI_COMMIT_TAG=release-request/v1.2.3", "CI_COMMIT_SHA=" + strings.Repeat("a", 40),
		"CI_DEFAULT_BRANCH=main", "RC_RELEASE_BRANCH_INPUT=bad..name",
	}

	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "not a valid branch name") {
		t.Fatalf("release context accepted an invalid branch: %v\n%s", err, output)
	}
}

// runReleasePrepare runs rc_prepare_release with inert engine and git
// functions under hostile branch variables, returning its combined output.
func runReleasePrepare(t *testing.T, script, input string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "_release-render"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"CHANGELOG.md", "commit-msg.txt"} {
		if err := os.WriteFile(filepath.Join(dir, "_release-render", name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-c", //nolint:gosec // Reviewed script with inert engine and git functions.
		`set -eu; source "$1"; rc_engine() { printf 'engine %s\n' "$*"; }; git() { printf 'git %s\n' "$*"; }; rc_release_context; rc_request_checkout() { rc_engine validate tag commit --branch "${RC_RELEASE_BRANCH}"; }; rc_prepare_release`,
		"branch-guard", script)
	command.Dir = dir
	command.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + dir, "CI_JOB_NAME=Prepare: release commit and tag", "CI_PROJECT_DIR=" + dir,
		"CI_COMMIT_TAG=release-request/v1.2.3", "CI_COMMIT_SHA=" + strings.Repeat("a", 40), "CI_PROJECT_PATH=team/app",
		"RELEASE_SSH_SIGNING_KEY=inert", "RC_GIT_URL=ssh://git@gitlab.invalid/team/app.git", "RC_GIT_HOST_KEY=SHA256:x",
		"RC_BOT_NAME=bot", "RC_BOT_EMAIL=bot@invalid.example", "CI_DEFAULT_BRANCH=main", "RC_RELEASE_BRANCH_INPUT=" + input,
		"RC_RELEASE_BRANCH=hostile", "RELEASE_BRANCH=hostile", "BRANCH=hostile",
	}

	output, err := command.CombinedOutput()

	return string(output), err
}
