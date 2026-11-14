// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The real orchestration must reject misplaced credentials before it invokes
// even the trusted CLI. These cases never start a network tool or a signer.
func TestReleaseJobRejectsCredentialsOutsideItsRole(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{"Check: release request", "RELEASE_SSH_SIGNING_KEY"},
		{"Check: release request", "RELEASE_REGISTRY_TOKEN"},
		{"Check: consumer checks", "RELEASE_SSH_SIGNING_KEY"},
		{"Check: consumer checks", "RELEASE_COSIGN_PRIVATE_KEY"},
		{"Check: consumer checks", "RELEASE_REGISTRY_TOKEN"},
		{"Check: consumer checks", "RELEASE_TOKEN"},
		{"Prepare: changelog", "RELEASE_COSIGN_PRIVATE_KEY"},
		{"Prepare: version java", "RELEASE_SSH_SIGNING_KEY"},
		{"Build: node", "RELEASE_TOKEN"},
		{"Build: release files", "RELEASE_TOKEN"},
		{"Build: release files", "RELEASE_REGISTRY_TOKEN"},
		{"Build: export amd64", "RELEASE_REGISTRY_TOKEN"},
		{"Images: build amd64", "RELEASE_REGISTRY_TOKEN"},
		{"Sign: files and images", "RELEASE_SSH_SIGNING_KEY"},
		{"Sign: files and images", "RELEASE_TOKEN"},
		{"Publish: images", "RELEASE_COSIGN_PRIVATE_KEY"},
		{"Publish: release", "RELEASE_GPG_PRIVATE_KEY"},
		{"Publish: tidy up", "RELEASE_SSH_SIGNING_KEY"},
		{"Summary: release", "RELEASE_TOKEN"},
	} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			t.Parallel()

			ctx := commandContext(t)

			script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
			command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c", `source "$1"; rc_engine() { echo engine-called; }; rc_release_context`, "release-guard", script)
			command.Dir = t.TempDir()
			// Named by a project variable as the job that holds the secret.
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + command.Dir, "rc_job_role=" + releaseJobRoles[pair[0]],
				"CI_JOB_NAME=" + releaseSecretHolders[pair[1]], pair[1] + "=inert-canary-value"}

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

	ctx := commandContext(t)

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")

	const invocation = `set -eu; source "$1"; engine_status=$3; rc_engine() {
if [ "$engine_status" = owner ]; then OWNER_CLI_HELPER=1 "$OWNER_CLI_BINARY" -test.run='^TestOwnerCLI$' -- "$@"; return; fi
echo engine-called; if [ "$engine_status" != 0 ]; then return "$engine_status"; fi; mkdir dist
}; if rc_read_handoff "$2"; then exit 0; else exit $?; fi`

	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c", invocation, "handoff-guard", script, dir, "owner")
	command.Dir = dir
	command.Env = []string{"PATH=" + componentTools(t), "HOME=" + dir, "OWNER_CLI_BINARY=" + os.Args[0]}

	output, err := command.CombinedOutput()
	if err == nil || strings.Contains(string(output), "engine-called") {
		t.Fatal("invalid digest was not refused by the Go owner")
	}

	if _, err := os.Stat(filepath.Join(dir, "exploited")); !os.IsNotExist(err) {
		t.Fatal("handoff data was evaluated as shell")
	}

	if err := os.WriteFile(filepath.Join(dir, "digest"), []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(dir, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{"17", "0"} {
		control := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c", invocation, "handoff-guard", script, dir, status)
		control.Dir, control.Env = t.TempDir(), command.Env

		output, err := control.CombinedOutput()
		if !strings.Contains(string(output), "engine-called") {
			t.Fatalf("valid digest did not reach validation: %v\n%s", err, output)
		}

		if status != "0" {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 17 {
				t.Fatalf("engine failure lost its status: %v\n%s", err, output)
			}

			if _, statErr := os.Stat(filepath.Join(control.Dir, "dist")); !os.IsNotExist(statErr) {
				t.Fatal("rejected handoff was copied")
			}

			continue
		}

		if err != nil {
			t.Fatalf("valid handoff was refused: %v\n%s", err, output)
		}

		if _, statErr := os.Stat(filepath.Join(control.Dir, "dist")); statErr != nil {
			t.Fatal(statErr)
		}
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

			ctx := commandContext(t)

			script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
			command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-eu", "-c", `source "$1"; rc_publication_scope`, "publication-scope", script)

			command.Env = []string{"PATH=/usr/bin:/bin", "RC_REGISTRY=registry.invalid", "CI_PROJECT_PATH=owner/source",
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

	body, err := os.ReadFile(script)
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

	ctx := commandContext(t)

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c",
		`set -eu; source "$1"; rc_engine() { printf 'engine %s\n' "$*"; }; rc_release_context`, "branch-name", script)
	command.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "rc_job_role=prepare",
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
func runReleasePrepare(t *testing.T, script, input string, extraEnv ...string) (string, error) {
	t.Helper()

	return runReleasePrepareIn(t, t.TempDir(), script, input, extraEnv...)
}

func runReleasePrepareIn(t *testing.T, dir, script, input string, extraEnv ...string) (string, error) {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(dir, "_release-render"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"CHANGELOG.md", "commit-msg.txt"} {
		if err := os.WriteFile(filepath.Join(dir, "_release-render", name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctx := commandContext(t)

	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c",
		`set -eu; source "$1"; rc_engine() { printf 'engine %s\n' "$*"; local previous='' arg path; for arg in "$@"; do if [ "$previous" = --paths-file ]; then : >"$arg"; for path in ${FAKE_BUMP_PATHS:-}; do printf '%s\n' "$path" >>"$arg"; done; fi; previous=$arg; done; }; git() { printf 'git %s\n' "$*"; }; rc_release_context; rc_request_checkout() { rc_engine validate tag commit --branch "${RC_RELEASE_BRANCH}"; }; rc_prepare_release`,
		"branch-guard", script)
	command.Dir = dir
	command.Env = []string{
		"PATH=" + componentTools(t), "HOME=" + dir, "rc_job_role=prepare", "CI_PROJECT_DIR=" + dir,
		"CI_COMMIT_TAG=release-request/v1.2.3", "CI_COMMIT_SHA=" + strings.Repeat("a", 40), "CI_PROJECT_PATH=team/app",
		"RELEASE_SSH_SIGNING_KEY=inert", "RC_GIT_URL=ssh://git@gitlab.invalid/team/app.git", "RC_GIT_HOST_KEY=SHA256:x",
		"RC_BOT_NAME=bot", "RC_BOT_EMAIL=bot@invalid.example", "CI_DEFAULT_BRANCH=main", "RC_RELEASE_BRANCH_INPUT=" + input,
		"RC_RELEASE_BRANCH=hostile", "RELEASE_BRANCH=hostile", "BRANCH=hostile", "RC_RELEASE_POLICY=stable",
		"RC_WORK=" + dir, "RC_ARTIFACTS_CONFIG=", "RC_BASE_STAGES=", "RC_IMAGE_BUILDER=buildah",
	}
	command.Env = append(command.Env, extraEnv...)

	output, err := command.CombinedOutput()

	return string(output), err
}

// An empty release-git-url leaves --git-url out, so the engine pushes to this
// project on the GitLab web host; a given one is passed as written.
func TestReleasePreparePassesTheGitURLOnlyWhenGiven(t *testing.T) {
	t.Parallel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")

	for url, want := range map[string]string{
		"ssh://git@gitlab.invalid:2222/team/app.git": "--git-url ssh://git@gitlab.invalid:2222/team/app.git --host-key-fingerprint",
		"": "--repository team/app --host-key-fingerprint",
	} {
		output, err := runReleasePrepareIn(t, t.TempDir(), script, "", "RC_GIT_URL="+url)
		if err != nil || !strings.Contains(output, "engine version commit-changelog-release") || !strings.Contains(output, want) {
			t.Errorf("RC_GIT_URL %q: err %v, want %q in\n%s", url, err, want, output)
		}
	}
}

// release-prepare runs no build tool: it joins the patches the plan's
// key-free version jobs made, each checked against its job's digest, hands
// the joined patch with its digest and the plan to the engine's text check,
// and commits the paths the engine says it wrote beside the changelog, not
// whatever else the checkout holds, such as commit-msg.txt. Without a version
// job it hands an empty patch, so the engine runs no native tool either, and
// a patch that differs from its digest stops the release.
func TestReleasePrepareAppliesOnlyAVerifiedVersionBump(t *testing.T) {
	t.Parallel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
	patches := map[string]string{"java": "diff --git a/services/backend/pom.xml b/services/backend/pom.xml\n", "node": "diff --git a/packages/frontend/package.json b/packages/frontend/package.json\n"}

	handoffs := func(t *testing.T, dir string, digests map[string]string) {
		t.Helper()

		for family, patch := range patches {
			bump := filepath.Join(dir, "_release-bump-"+family)
			if err := os.MkdirAll(bump, 0o700); err != nil {
				t.Fatal(err)
			}

			for name, body := range map[string]string{"version-bump.patch": patch, "digest": digests[family] + "\n"} {
				if err := os.WriteFile(filepath.Join(bump, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	digestOf := func(body string) string {
		sum := sha256.Sum256([]byte(body))

		return hex.EncodeToString(sum[:])
	}

	t.Run("bump", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		handoffs(t, dir, map[string]string{"java": digestOf(patches["java"]), "node": digestOf(patches["node"])})

		output, err := runReleasePrepareIn(t, dir, script, "", "FAKE_BUMP_PATHS=services/backend/pom.xml packages/frontend/package.json")
		if err != nil {
			t.Fatalf("prepare did not run: %v\n%s", err, output)
		}

		joined := patches["java"] + patches["node"]
		for _, want := range []string{
			"engine plan gitlab-release-pipeline --base-stages  --image-builder buildah --prepare-plan-output " + dir + "/prepare-plan.json",
			"engine version bump-plan --plan-json  --version 1.2.3 --release-policy stable --patch " + dir + "/version-bump.patch --patch-sha256 " + digestOf(joined) +
				" --paths-file " + dir + "/bump-paths",
			"--include-paths-file " + dir + "/bump-paths",
		} {
			if !strings.Contains(output, want) {
				t.Errorf("prepare output lacks %q:\n%s", want, output)
			}
		}

		if body, err := os.ReadFile(filepath.Join(dir, "version-bump.patch")); err != nil || string(body) != joined {
			t.Errorf("the joined patch is %q (%v), want the java then the node patch", body, err)
		}

		if paths, err := os.ReadFile(filepath.Join(dir, "bump-paths")); err != nil || string(paths) != "services/backend/pom.xml\npackages/frontend/package.json\n" {
			t.Errorf("bump paths are %q (%v)", paths, err)
		}

		if strings.Contains(output, "--project-type") || strings.Contains(output, "engine version bump ") {
			t.Errorf("release-prepare ran a build tool itself:\n%s", output)
		}
	})

	t.Run("no bump", func(t *testing.T) {
		t.Parallel()

		output, err := runReleasePrepare(t, script, "")
		if err != nil || !strings.Contains(output, "--patch-sha256 "+digestOf("")) || strings.Contains(output, "--include-paths-file") {
			t.Fatalf("prepare without a version job = %v:\n%s", err, output)
		}
	})

	t.Run("a patch that is not its job's", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		handoffs(t, dir, map[string]string{"java": digestOf(patches["java"]), "node": strings.Repeat("b", 64)})

		output, err := runReleasePrepareIn(t, dir, script, "")
		if err == nil || !strings.Contains(output, "node version patch differs") || strings.Contains(output, "commit-changelog-release") {
			t.Fatalf("prepare went on with a foreign patch:\n%s", output)
		}
	})
}

// A path guard of the trusted release script stops the job. The bare
// `test -f x && test ! -L x` it replaces stopped nothing under set -e when its
// first test failed: a preseeded dist or publication ledger went through.
func TestReleasePathGuardsStopTheJob(t *testing.T) {
	t.Parallel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")

	for name, test := range map[string]struct {
		setup, call string
		stops       bool
	}{
		"absent path is required absent":    {call: `rc_require_absent missing`},
		"existing path is required absent":  {setup: `mkdir present`, call: `rc_require_absent present`, stops: true},
		"link is required absent":           {setup: `ln -s nowhere link`, call: `rc_require_absent link`, stops: true},
		"regular file is required regular":  {setup: `printf x >file`, call: `rc_require_regular_file file`},
		"missing file is required regular":  {call: `rc_require_regular_file missing`, stops: true},
		"link is required regular":          {setup: `printf x >file; ln -s file link`, call: `rc_require_regular_file link`, stops: true},
		"preseeded dist stops the hand-off": {setup: `mkdir -p hand/dist dist; printf '%064d' 0 >hand/digest`, call: `rc_read_handoff hand`, stops: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			setup := test.setup
			if setup == "" {
				setup = ":"
			}

			ctx := commandContext(t)

			command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c",
				`set -euo pipefail; source "$1"; `+ownerCLI+setup+`; `+test.call+`; echo went-on`, "guards", script)
			command.Dir = t.TempDir()
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + command.Dir, "OWNER_CLI_BINARY=" + os.Args[0]}

			output, err := command.CombinedOutput()
			if stopped := err != nil && !strings.Contains(string(output), "went-on"); stopped != test.stops {
				t.Fatalf("stopped %v, want %v:\n%s", stopped, test.stops, output)
			}
		})
	}
}

// releaseSecretHolders is a job each release secret belongs to.
var releaseSecretHolders = map[string]string{
	"RELEASE_SSH_SIGNING_KEY":    "Prepare: release commit and tag",
	"RELEASE_COSIGN_PRIVATE_KEY": "Sign: files and images",
	"RELEASE_GPG_PRIVATE_KEY":    "Sign: files and images",
	"RELEASE_REGISTRY_TOKEN":     "Sign: files and images",
	"RELEASE_TOKEN":              "Publish: release",
	"RELEASE_PACKAGE_TOKEN":      "Publish: npm packages",
}

// The package job holds the package token and no other release secret, and
// no other job holds the package token: a job that runs the project's code,
// or signs, could otherwise publish a package, and the job that hands
// packages to npm could sign.
func TestPackageCredentialBelongsToThePackageJobAlone(t *testing.T) {
	t.Parallel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
	refused := func(t *testing.T, role, secret string) bool {
		t.Helper()

		command := exec.CommandContext(commandContext(t), componentBash(t), "--noprofile", "--norc", "-c",
			`source "$1"; rc_engine() { :; }; rc_release_context`, "release-guard", script)
		command.Dir = t.TempDir()
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + command.Dir, "rc_job_role=" + role, secret + "=inert-canary-value",
			"CI_COMMIT_TAG=release-request/v1.2.3", "CI_COMMIT_SHA=" + strings.Repeat("a", 40), "CI_DEFAULT_BRANCH=main", "RC_RELEASE_POLICY=policy"}

		output, err := command.CombinedOutput()
		wrong := strings.Contains(string(output), secret+" is available to the wrong release job")

		if wrong == (err == nil) {
			t.Fatalf("role %s with %s: %v\n%s", role, secret, err, output)
		}

		return wrong
	}

	for job, role := range releaseJobRoles {
		if refused(t, role, "RELEASE_PACKAGE_TOKEN") != (role != "packages") {
			t.Errorf("%s (role %s) and the package token: want it held by the package job alone", job, role)
		}
	}

	for _, secret := range []string{
		"RELEASE_SSH_SIGNING_KEY", "RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_COSIGN_PASSPHRASE", "RELEASE_GPG_PRIVATE_KEY",
		"RELEASE_GPG_PASSPHRASE", "RELEASE_TOKEN", "RELEASE_REGISTRY_TOKEN", "SSH_SIGNING_KEY", "COSIGN_KEY", "COSIGN_PASSWORD",
		"GPG_SIGNING_KEY", "GPG_SIGNING_PASSWORD", "GITLAB_TOKEN",
	} {
		if !refused(t, "packages", secret) {
			t.Errorf("the package job may hold %s", secret)
		}
	}

	// The job is the signer runner's, in the environment the token is scoped
	// to, and publishes only once the release is: after the signer's
	// hand-off, which it publishes from, and the release's publication.
	body := releaseJobsBodyFor(t, "buildah")
	job := gitlabrender.ResolveExtends(t, body, body[".forge-ci-release-publish-npm-packages"].(map[string]any))

	if job["tags"] != "$[[ inputs.signer-runner-tags ]]" {
		t.Errorf("the package job's tags are %v, want the signer runner's", job["tags"])
	}

	if environment, _ := job["environment"].(map[string]any); environment["name"] != "release/packages" || environment["action"] != "prepare" {
		t.Errorf("the package job's environment is %v", job["environment"])
	}

	wantNeeds := []any{
		map[string]any{"job": "Sign: files and images", "artifacts": true},
		map[string]any{"job": "Publish: release", "artifacts": false},
	}
	if needs, _ := job["needs"].([]any); !reflect.DeepEqual(needs, wantNeeds) {
		t.Errorf("the package job needs %v, want %v", job["needs"], wantNeeds)
	}
}

// Every job keeps the secrets its role needs: the policy is written in roles,
// and each job's start gives it its role.
func TestReleaseJobKeepsTheCredentialsOfItsRole(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{"Prepare: release commit and tag", "RELEASE_SSH_SIGNING_KEY"},
		{"Sign: files and images", "RELEASE_COSIGN_PRIVATE_KEY"},
		{"Sign: files and images", "RELEASE_GPG_PRIVATE_KEY"},
		{"Sign: files and images", "RELEASE_GPG_PASSPHRASE"},
		{"Sign: files and images", "RELEASE_REGISTRY_TOKEN"},
		{"Publish: release", "RELEASE_TOKEN"},
		{"Publish: images", "RELEASE_REGISTRY_TOKEN"},
		{"Publish: tidy up", "RELEASE_REGISTRY_TOKEN"},
		{"Check: signing access", "RELEASE_COSIGN_PRIVATE_KEY"},
		{"Check: publishing access", "RELEASE_TOKEN"},
		{"Check: registry access", "RELEASE_REGISTRY_TOKEN"},
	} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			t.Parallel()

			ctx := commandContext(t)

			script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
			command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c",
				`source "$1"; rc_engine() { :; }; rc_release_context`, "release-guard", script)
			command.Dir = t.TempDir()
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + command.Dir, "rc_job_role=" + releaseJobRoles[pair[0]], pair[1] + "=inert-canary-value",
				"CI_COMMIT_TAG=release-request/v1.2.3", "CI_COMMIT_SHA=" + strings.Repeat("a", 40), "CI_DEFAULT_BRANCH=main"}

			output, err := command.CombinedOutput()
			if strings.Contains(string(output), "is available to the wrong release job") {
				t.Fatalf("the role's own credential was refused: %v\n%s", err, output)
			}
		})
	}
}

// A family's version job bumps the project type the plan names for its
// family, and hands on the diff of the version files its own bump names, its
// file-pattern without the changelog copies, so two families' patches never
// both carry one file.
func TestReleaseVersionJobDiffsOnlyItsFamilyFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "_release-render"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "_release-render", "CHANGELOG.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := commandContext(t)

	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-eu", "-c",
		`source "$1"; rc_request_checkout() { :; }; rc_new_handoff() { mkdir -- "$1"; }
rc_engine() { printf 'engine %s\n' "$*"; case "$*" in *bump-plan*) printf 'FILE_PATTERN=services/backend/CHANGELOG.md :(glob)services/backend/**/pom.xml\n' >"${CI_OUTPUT}" ;;
  *"--family java --project-type-output ${RC_WORK}/project-type"*) printf 'maven\n' >"${RC_WORK}/project-type" ;; esac; }
git() { printf 'git %s\n' "$*"; }; rc_bump_version java`,
		"bump", filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh"))
	command.Dir = dir
	command.Env = []string{"PATH=" + componentTools(t), "CI_PROJECT_DIR=" + dir, "RC_WORK=" + dir, "RC_ARTIFACTS_CONFIG=", "RC_BASE_STAGES=", "RC_IMAGE_BUILDER=buildah",
		"RELEASE_VERSION=1.2.3", "RELEASE_POLICY=stable"}

	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("version job failed: %v\n%s", err, out)
	}

	patch, err := os.ReadFile(filepath.Join(dir, "_release-bump-java", "version-bump.patch"))
	if err != nil || !strings.Contains(string(out), "--project-type maven") || !strings.HasSuffix(string(patch), "--dst-prefix=b/ -- :(glob)services/backend/**/pom.xml\n") {
		t.Errorf("the version job diffed more than its family's files: %v\n%s\n%s", err, patch, out)
	}
}

// npm gives npm-shrinkwrap.json precedence over package-lock.json, and the
// release commit refuses a patch that leaves out the lockfile npm wrote. The
// node family's job, with real Git and the engine's npm file pattern, hands
// on a patch with the shrinkwrap file that the release verification accepts.
func TestReleaseNodeVersionJobCarriesTheShrinkwrapFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gitEnv := []string{"PATH=" + componentTools(t), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	project := func(release string) {
		for _, name := range []string{"package.json", "npm-shrinkwrap.json"} {
			if err := os.WriteFile(filepath.Join(dir, "frontend", name), []byte(`{"name":"frontend","version":"`+release+`"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	gitRun := func(args ...string) {
		command := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		command.Dir, command.Env = dir, gitEnv

		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	for _, sub := range []string{"frontend", "_release-render"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "_release-render", "CHANGELOG.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	project("1.0.0")
	gitRun("init", "-q", "-b", "main")
	gitRun("add", "--", "frontend")
	gitRun("commit", "-q", "-m", "initial")
	project("1.2.3")

	var pattern strings.Builder
	for file := range strings.FieldsSeq(version.FilePattern(projecttype.NPM)) {
		pattern.WriteString(" frontend/" + file)
	}

	ctx := commandContext(t)

	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-eu", "-c",
		`source "$1"; rc_request_checkout() { :; }; rc_new_handoff() { mkdir -- "$1"; }
rc_engine() { case "$*" in *bump-plan*) printf 'FILE_PATTERN=%s\n' "${FAKE_PATTERN# }" >"${CI_OUTPUT}" ;;
  *"--family node --project-type-output ${RC_WORK}/project-type"*) printf 'npm\n' >"${RC_WORK}/project-type" ;; esac; }; rc_bump_version node`,
		"bump", filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh"))
	command.Dir = dir
	command.Env = append(slices.Clone(gitEnv), "CI_PROJECT_DIR="+dir, "RC_WORK="+dir, "RC_ARTIFACTS_CONFIG=", "RC_BASE_STAGES=", "RC_IMAGE_BUILDER=buildah", "RELEASE_VERSION=1.2.3", "RELEASE_POLICY=stable", "FAKE_PATTERN="+pattern.String())

	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("version job failed: %v\n%s", err, out)
	}

	patch, err := os.ReadFile(filepath.Join(dir, "_release-bump-node", "version-bump.patch"))
	if err != nil {
		t.Fatal(err)
	}

	files, _ := version.BumpPatchFiles(projecttype.NPM)

	result, err := version.VerifyBumpPatch(patch, files, "1.2.3")
	if err != nil || !strings.Contains(string(patch), "b/frontend/npm-shrinkwrap.json") {
		t.Fatalf("the node patch lost the shrinkwrap file or fails verification: %v %+v\n%s", err, result, patch)
	}
}
