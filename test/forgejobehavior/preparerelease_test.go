// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

type prepareReleaseWorkflow struct {
	On struct {
		WorkflowCall struct {
			Inputs map[string]actionInput `yaml:"inputs"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Uses  string         `yaml:"uses"`
		With  map[string]any `yaml:"with"`
		Steps []step         `yaml:"steps"`
	} `yaml:"jobs"`
}

func prepareReleaseRequired(inputs map[string]actionInput) []string {
	var names []string

	for name, input := range inputs {
		if input.Required {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return names
}

// The reusable workflow and the consumer template pass every required input
// down the chain, and the security-relevant values are bound to the trusted
// event context rather than left to the caller.
func TestPrepareReleaseInputWiring(t *testing.T) {
	t.Parallel()
	actionInputs := loadAction(t, "prepare-release").Inputs

	var workflow, template prepareReleaseWorkflow
	loadYAML(t, ".forgejo/workflows/prepare-release.yml", &workflow)
	loadYAML(t, "examples/go-cli/forgejo/release.yml", &template)

	workflowInputs := workflow.On.WorkflowCall.Inputs
	if len(workflowInputs) == 0 {
		t.Fatal("prepare-release workflow declares no workflow_call inputs")
	}

	var call *step

	for i, step := range workflow.Jobs["prepare"].Steps {
		if step.Uses == "./.forge-ci-helpers/.forgejo/actions/prepare-release" {
			call = &workflow.Jobs["prepare"].Steps[i]

			break
		}
	}

	if call == nil {
		t.Fatal("the prepare job does not call the local prepare-release action")
	}

	for _, name := range prepareReleaseRequired(actionInputs) {
		if _, ok := call.With[name]; !ok {
			t.Errorf("reusable workflow omits required prepare-release input %s", name)
		}
	}

	templateCall := template.Jobs["prepare"]
	if !strings.Contains(templateCall.Uses, "/.forgejo/workflows/prepare-release.yml@") {
		t.Fatalf("consumer template prepare job calls %q, not the reusable prepare workflow", templateCall.Uses)
	}

	for _, name := range prepareReleaseRequired(workflowInputs) {
		if _, ok := templateCall.With[name]; !ok {
			t.Errorf("consumer template omits required prepare workflow input %s", name)
		}
	}

	for _, check := range []struct {
		what      string
		got, want any
	}{
		{"signature verification default", workflowInputs["verify-release-request-signature"].Default, "false"},
		{"allowed signers default", workflowInputs["release-request-allowed-signers"].Default, ".forgejo/release-allowed-signers"},
		{"forwarded signature verification", call.With["verify-release-request-signature"], "${{ inputs['verify-release-request-signature'] }}"},
		{"forwarded allowed signers", call.With["release-request-allowed-signers"], "${{ inputs['release-request-allowed-signers'] }}"},
		{"request SHA bound to the event commit", call.With["release-request-sha"], "${{ forgejo.sha }}"},
		{"changelog links bound to the trusted repository", workflowStep(t, "prepare-release.yml", "render", "Render project-controlled changelog configuration").With["repository-url"], "${{ forgejo.server_url }}/${{ forgejo.repository }}"},
		{"template disables distro-package secrets", templateCall.With["sign-distro-packages"], "false"},
		{"template disables container secrets", templateCall.With["publish-container-images"], "false"},
		{"template exposes signature verification", templateCall.With["verify-release-request-signature"], "false"},
		{"template exposes allowed signers", templateCall.With["release-request-allowed-signers"], ".forgejo/release-allowed-signers"},
		{"template disables distro-package signing", template.Jobs["sign-and-publish"].With["sign-distro-packages"], "false"},
	} {
		if check.got != check.want {
			t.Errorf("%s: %v, want %q", check.what, check.got, check.want)
		}
	}

	if _, ok := workflowInputs["repository-url"]; ok {
		t.Error("reusable prepare workflow exposes repository-url as a caller override")
	}

	checkoutBound := false
	for _, step := range workflow.Jobs["prepare"].Steps {
		checkoutBound = checkoutBound || step.Env["CHECKOUT_REF"] == "${{ forgejo.sha }}"
	}

	if !checkoutBound {
		t.Error("the prepare job does not check out the immutable event commit")
	}
}

// Only the signing step holds the SSH key, it runs after the key-free request
// revalidation and before the key-free validation, and it hands the engine a
// scrubbed environment.
func TestPrepareReleaseSigningKeyBoundary(t *testing.T) {
	t.Parallel()

	const (
		revalidate = "Revalidate release request before loading signing key"
		sign       = "Sign changelog commit and create release tag"
		validate   = "Validate prepared release without signing key"
	)

	steps := loadAction(t, "prepare-release").Runs.Steps

	index := map[string]int{}
	for i, step := range steps {
		index[step.Name] = i
		if _, holds := step.Env["SSH_SIGNING_KEY"]; holds != (step.Name == sign) {
			t.Errorf("step %q SSH_SIGNING_KEY presence is %t", step.Name, holds)
		}
	}

	for _, name := range []string{revalidate, sign, validate} {
		if _, ok := index[name]; !ok {
			t.Fatalf("prepare-release has no step %q", name)
		}
	}

	if index[revalidate] >= index[sign] || index[sign] >= index[validate] {
		t.Fatalf("step order revalidate=%d sign=%d validate=%d", index[revalidate], index[sign], index[validate])
	}
	// The engine reads the Git identity and both trust anchors from these
	// variables, so each must come from its own input.
	if got, want := steps[index[sign]].Env, map[string]string{
		"SSH_SIGNING_KEY":                  "${{ inputs.ssh-signing-key }}",
		"GIT_USER_NAME":                    "${{ inputs.bot-name }}",
		"GIT_USER_EMAIL":                   "${{ inputs.bot-email }}",
		"RELEASE_GIT_URL":                  "${{ inputs.release-git-url }}",
		"RELEASE_GIT_HOST_KEY_FINGERPRINT": "${{ inputs.release-git-host-key-fingerprint }}",
		"FORGEJO_REF_NAME":                 "${{ steps.derive-release-context.outputs.release-tag }}",
		"FORGEJO_REPO":                     "${{ inputs.forgejo-repo }}",
		"SIGNING_EXECUTABLE":               "${{ steps.prepare-signing.outputs.signing-executable }}",
		"TARGET_BRANCH":                    "${{ inputs.release-branch }}",
		"INCLUDE_PATHS_FILE":               "${{ inputs.include-paths-file }}",
	}; !maps.Equal(got, want) {
		t.Fatalf("signing step env = %q, want %q", got, want)
	}

	const key = "fixture-key-DO-NOT-LOG"

	signEnv := func(s *sandbox, executable, status string) []string {
		return []string{
			"SIGNING_EXECUTABLE=" + executable, "FAKE_SIGNER_STATUS=" + status, "SSH_SIGNING_KEY=" + key,
			"FORGEJO_REF_NAME=v1.2.3", "FORGEJO_REPO=owner/repo", "TARGET_BRANCH=stable", "RELEASE_GIT_URL=ssh://git@forgejo.invalid/owner/repo.git",
			"RELEASE_GIT_HOST_KEY_FINGERPRINT=SHA256:fixture", "GIT_USER_NAME=Fixture Bot", "GIT_USER_EMAIL=bot@invalid.example",
			"BASH_ENV=" + s.write("bash-env", ":\n", 0o600), "ENV=/dev/null",
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/hooks", "GIT_CONFIG_GLOBAL=/dev/null",
		}
	}

	const signer = `#!/usr/bin/env bash
printf '%s\0' "$@" >"${FAKE_ARGV_LOG:?}"
env >"${FAKE_ARGV_LOG:?}.env"
exit "${FAKE_SIGNER_STATUS:-0}"
`

	t.Run("signing step runs the verified executable with a scrubbed environment", func(t *testing.T) {
		t.Parallel()
		box := newSandbox(t, "prepare-release")
		got := box.run(loadAction(t, "prepare-release").script(t, sign), signEnv(box, box.write(".forgejo/bin/signer", signer, 0o700), "0")...)

		want := []string{"version", "commit-changelog-release", "--tag", "v1.2.3", "--branch", "stable", "--repository", "owner/repo",
			"--git-url", "ssh://git@forgejo.invalid/owner/repo.git"}
		if got.status != 0 || !slices.Equal(box.argv(), want) {
			t.Fatalf("status %d argv %q; stderr %s", got.status, box.argv(), got.stderr)
		}

		if strings.Contains(got.stdout+got.stderr, key) {
			t.Fatal("the signing key reached the step log")
		}

		env, err := os.ReadFile(box.argvLog + ".env")
		if err != nil {
			t.Fatal(err)
		}

		lines := strings.Split(string(env), "\n")
		for _, want := range []string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "SSH_SIGNING_KEY=" + key,
			"RELEASE_GIT_HOST_KEY_FINGERPRINT=SHA256:fixture", "GIT_USER_NAME=Fixture Bot", "GIT_USER_EMAIL=bot@invalid.example",
		} {
			if !slices.Contains(lines, want) {
				t.Errorf("signing environment lacks %q", want)
			}
		}

		if slices.ContainsFunc(lines, func(line string) bool {
			return strings.HasPrefix(line, "BASH_ENV=") || strings.HasPrefix(line, "ENV=") || strings.HasPrefix(line, "GIT_CONFIG_")
		}) {
			t.Errorf("signing environment keeps a startup or Git config variable:\n%s", env)
		}
	})

	t.Run("signing step propagates the signing status", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t, "prepare-release")

		got := s.run(loadAction(t, "prepare-release").script(t, sign), signEnv(s, s.write(".forgejo/bin/signer", signer, 0o700), "42")...)
		if got.status != 42 {
			t.Fatalf("status %d, want 42; stderr %s", got.status, got.stderr)
		}
	})

	t.Run("signing step refuses a missing signing executable", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t, "prepare-release")

		got := s.run(loadAction(t, "prepare-release").script(t, sign), signEnv(s, "", "0")...)
		if got.status == 0 || !strings.Contains(got.stderr, "SIGNING_EXECUTABLE") {
			t.Fatalf("status %d; stderr %s", got.status, got.stderr)
		}
	})
}

// The key-free render step hands the engine its inputs, each as one argument,
// keeps the SSH signing key from the resolver and the engine, refuses a
// missing repository URL before starting the engine, and propagates the
// engine status. Rendering belongs to the engine and its companion suite.
func TestPrepareReleaseRenderChangelog(t *testing.T) {
	t.Parallel()
	script := loadAction(t, "render-changelog").script(t, "Generate changelog without SSH key")

	const (
		key      = "ssh-signing-key-DO-NOT-LOG"
		trailers = "Release-Request: release-request/v1.2.4\nCo-authored-by: Ada <ada@invalid.example>"
	)

	run := func(t *testing.T, url, status string) (*sandbox, result) {
		t.Helper()
		box := newSandbox(t, "prepare-release")
		env := append(installRecordingFakes(box), "RUNNER_TEMP="+t.TempDir(), "SSH_SIGNING_KEY="+key,
			"FAKE_ENGINE_STATUS="+status, "CHANGELOG_BACKEND=git-cliff", "CHANGELOG_CONFIG=cliff.toml",
			"COMMIT_BODY_CONFIG=body.toml", "CHANGELOG_REPOSITORY_URL="+url, "FORGEJO_REF_NAME=v1.2.4",
			"COMMIT_TRAILERS="+trailers, "TARGET_BRANCH=stable")

		return box, box.run(script, env...)
	}

	t.Run("forwards trailers as one argument and hides the signing key", func(t *testing.T) {
		t.Parallel()
		box, got := run(t, "https://forgejo.invalid/owner/repo", "0")

		want := []string{"version", "render-changelog", "--branch", "stable", "--backend", "git-cliff", "--tag", "v1.2.4",
			"--repository-url", "https://forgejo.invalid/owner/repo", "--changelog-config", "cliff.toml",
			"--commit-body-config", "body.toml", "--commit-trailers", trailers}
		if got.status != 0 || !slices.Equal(box.argv(), want) {
			t.Fatalf("status %d argv %q; stderr %s", got.status, box.argv(), got.stderr)
		}

		r := recorded(box)
		if _, ok := r.env["SSH_SIGNING_KEY"]; ok || slices.Contains(r.shellVars, "SSH_SIGNING_KEY") {
			t.Error("SSH_SIGNING_KEY reached the resolver or the engine")
		}

		if strings.Contains(got.stdout+got.stderr+box.output(), key) {
			t.Error("the signing key reached the step log or outputs")
		}
	})

	t.Run("refuses a missing repository URL before the engine", func(t *testing.T) {
		t.Parallel()

		s, got := run(t, "", "0")
		if got.status == 0 || s.argv() != nil || !strings.Contains(got.stderr, "CHANGELOG_REPOSITORY_URL") {
			t.Fatalf("status %d argv %q; stderr %s", got.status, s.argv(), got.stderr)
		}
	})

	t.Run("propagates the engine status", func(t *testing.T) {
		t.Parallel()

		_, got := run(t, "https://forgejo.invalid/owner/repo", "42")
		if got.status != 42 {
			t.Fatalf("status %d, want 42; stderr %s", got.status, got.stderr)
		}
	})
}

// The revalidation step hands the engine the request, the final tag and the
// commit the caller took from the trusted event context, and asks for
// signature verification exactly when the consumer enabled it. The checks
// themselves are the engine's, tested in its own suite.
func TestPrepareReleaseRevalidatesRequestTag(t *testing.T) {
	t.Parallel()
	script := loadAction(t, "prepare-release").script(t, "Revalidate release request before loading signing key")

	const (
		request = "release-request/v1.2.3"
		commit  = "0123456789abcdef0123456789abcdef01234567"
	)

	base := []string{
		"RELEASE_REQUEST=" + request, "RELEASE_TAG=v1.2.3", "RELEASE_REQUEST_EVENT_SHA=" + commit,
		"RELEASE_REQUEST_ALLOWED_SIGNERS=/keys/allowed_signers",
	}

	for _, test := range []struct {
		name string
		env  []string
		argv []string
	}{
		{
			"verifies the signature when the consumer enabled it",
			[]string{"VERIFY_RELEASE_REQUEST_SIGNATURE=true"},
			[]string{"release", "validate-request", "--release-request", request, "--tag", "v1.2.3",
				"--expected-commit", commit, "--allowed-signers-file", "/keys/allowed_signers"},
		},
		{
			"keeps every other check when signature verification is off",
			[]string{"VERIFY_RELEASE_REQUEST_SIGNATURE=false"},
			[]string{"release", "validate-request", "--release-request", request, "--tag", "v1.2.3",
				"--expected-commit", commit, "--skip-signature"},
		},
		{
			"binds the reviewed input commit over the event commit",
			[]string{"VERIFY_RELEASE_REQUEST_SIGNATURE=false", "RELEASE_REQUEST_SHA_INPUT=" + strings.Repeat("b", 40)},
			[]string{"release", "validate-request", "--release-request", request, "--tag", "v1.2.3",
				"--expected-commit", strings.Repeat("b", 40), "--skip-signature"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := newSandbox(t, "prepare-release")

			got := s.run(script, append(append([]string{}, base...), test.env...)...)
			if got.status != 0 || !slices.Equal(s.argv(), test.argv) {
				t.Fatalf("status %d argv %q, want %q; stderr %s", got.status, s.argv(), test.argv, got.stderr)
			}
		})
	}

	t.Run("refuses a missing allowed-signers file before the engine", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t, "prepare-release")

		got := s.run(script, "RELEASE_REQUEST="+request, "RELEASE_TAG=v1.2.3",
			"RELEASE_REQUEST_EVENT_SHA="+commit, "VERIFY_RELEASE_REQUEST_SIGNATURE=true")
		if got.status == 0 || s.argv() != nil || !strings.Contains(got.stderr, "RELEASE_REQUEST_ALLOWED_SIGNERS") {
			t.Fatalf("status %d argv %q stderr %q", got.status, s.argv(), got.stderr)
		}
	})

	// A release whose final tag already exists is not resumed: the engine
	// refuses it here, so neither this step nor signing may be skipped.
	t.Run("always runs, and so does signing", func(t *testing.T) {
		t.Parallel()

		action := loadAction(t, "prepare-release")
		for _, name := range []string{"Revalidate release request before loading signing key", "Sign changelog commit and create release tag"} {
			if selected := action.step(t, name); selected.If != "" {
				t.Fatalf("step %q runs only if %q", name, selected.If)
			}
		}
	})
}

// The signing step runs only the engine this step verified, so verification
// happens before the key is loaded and the key step sources no library code.
func TestPrepareReleaseVerifiesTheSigningExecutable(t *testing.T) {
	t.Parallel()
	box := newSandbox(t, "prepare-release")
	box.write(".forgejo/bin/lib-forge-ci.sh", `verify_forge_ci() {
  HELPERS_ENGINE_VERIFIED=/verified/forge-ci
}
`, 0o600)

	got := box.run(loadAction(t, "prepare-release").script(t, "Prepare verified signing executable"))
	if got.status != 0 || box.output() != "signing-executable=/verified/forge-ci\n" {
		t.Fatalf("status %d outputs %q; stderr %s", got.status, box.output(), got.stderr)
	}
}

// The reusable workflow's first step refuses a release when a secret needed by
// an enabled feature group is empty, and names exactly the missing ones.
func TestPrepareReleaseSecretPreflight(t *testing.T) {
	t.Parallel()

	var workflow prepareReleaseWorkflow
	loadYAML(t, ".forgejo/workflows/prepare-release.yml", &workflow)

	var preflight *step

	for i, step := range workflow.Jobs["render"].Steps {
		if step.Name == "Check required secrets" {
			preflight = &workflow.Jobs["render"].Steps[i]
		}
	}

	if preflight == nil {
		t.Fatal("the render job has no secret preflight")
	}
	// Each SECRET_* carries presence, not the secret: the workflow sets it
	// from a "!= ''" expression, which answers false for an unset secret, so
	// a refusal cannot come from set -u on an undefined name either.
	var declared []string

	for name := range preflight.Env {
		if strings.HasPrefix(name, "SECRET_") {
			declared = append(declared, name+"=false")
		}
	}

	core := []string{"SECRET_RELEASE_TOKEN=true", "SECRET_RELEASE_COSIGN_PRIVATE_KEY=true", "SECRET_RELEASE_COSIGN_PASSPHRASE=true",
		"SECRET_RELEASE_SSH_SIGNING_KEY=true"}
	gpg := []string{"SECRET_RELEASE_GPG_PRIVATE_KEY=true", "SECRET_RELEASE_GPG_FINGERPRINT=true", "SECRET_RELEASE_GPG_PASSPHRASE=true"}
	join := slices.Concat[[]string]

	for _, test := range []struct {
		name, sign, publish string
		secrets             []string
		missing             string // empty: accepted
	}{
		{"core secrets with optional groups disabled", "false", "false", core, ""},
		{"every secret with both groups enabled", "true", "true", join(core, gpg, []string{"SECRET_RELEASE_REGISTRY_TOKEN=true"}), ""},
		{"distro packages without the GPG group", "true", "false", core,
			"  - RELEASE_GPG_PRIVATE_KEY\n  - RELEASE_GPG_FINGERPRINT\n  - RELEASE_GPG_PASSPHRASE\n"},
		{"container publication without the package token", "false", "true", core, "  - RELEASE_REGISTRY_TOKEN\n"},
		{"an independently missing GPG fingerprint", "true", "true",
			join(core, gpg, []string{"SECRET_RELEASE_GPG_FINGERPRINT=false", "SECRET_RELEASE_REGISTRY_TOKEN=true"}), "  - RELEASE_GPG_FINGERPRINT\n"},
		{"an independently missing core Cosign password", "false", "false",
			join(core, []string{"SECRET_RELEASE_COSIGN_PASSPHRASE=false"}), "  - RELEASE_COSIGN_PASSPHRASE\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := newSandbox(t, "prepare-release")
			env := join([]string{"SIGN_DISTRO_PACKAGES=" + test.sign, "PUBLISH_CONTAINER_IMAGES=" + test.publish}, declared, test.secrets)

			got := s.run(preflight.Run, env...)
			if test.missing == "" {
				if got.status != 0 || got.stderr != "" || got.stdout != "All required secrets are configured.\n" {
					t.Fatalf("status %d stdout %q stderr %q", got.status, got.stdout, got.stderr)
				}

				return
			}

			want := "ERROR: missing required secrets:\n" + test.missing + "Configure them in the repository or organization settings.\n"
			if got.status != 1 || got.stdout != "" || got.stderr != want {
				t.Fatalf("status %d stdout %q stderr %q, want %q", got.status, got.stdout, got.stderr, want)
			}
		})
	}
}

// preparedReleaseTag is the release tag the validation fixtures use.
const preparedReleaseTag = "v1.2.3"

// preparedReleaseFixture creates a Git repository in dir and returns its
// commits by name: request is the root, release and other are its children,
// stray is a child of other, and merge has the parents request and other. A
// non-empty tag names the commit the release tag points at. Nothing is signed.
func preparedReleaseFixture(t *testing.T, dir, tag string, lightweight bool) map[string]string {
	t.Helper()

	git := func(args ...string) string {
		//nolint:gosec // G204: git in the test's own fixture repository, with test-authored arguments.
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir,
			"-c", "user.name=Fixture Bot", "-c", "user.email=bot@invalid.example", "-c", "tag.gpgSign=false"}, args...)...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "LANG=C", "LC_ALL=C",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}

		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	git("init", "-q")

	tree := git("write-tree")
	// The name is the message, so siblings are distinct commits.
	commit := func(name string, parents ...string) string {
		return git(append([]string{"commit-tree", tree, "-m", name}, parents...)...)
	}

	commits := map[string]string{"request": commit("request")}
	commits["release"] = commit("release", "-p", commits["request"])
	commits["other"] = commit("other", "-p", commits["request"])
	commits["stray"] = commit("stray", "-p", commits["other"])
	commits["merge"] = commit("merge", "-p", commits["request"], "-p", commits["other"])

	switch {
	case tag == "":
	case lightweight:
		git("tag", preparedReleaseTag, commits[tag])
	default:
		git("tag", "-a", "-m", preparedReleaseTag, preparedReleaseTag, commits[tag])
	}

	return commits
}

// The key-free validation step publishes the release commit only when it is
// the single child of the authorized request commit and the release tag points
// at it. The request commit is the reviewed input when set, else the event
// commit.
func TestPrepareReleaseValidatesPreparedRelease(t *testing.T) {
	t.Parallel()
	script := loadAction(t, "prepare-release").script(t, "validate-release")

	const refusal = "ERROR: prepared release is not the single-child commit of the authorized request source\n"

	for _, test := range []struct {
		name         string
		signed, tag  string // fixture commit names; an empty tag creates none
		lightweight  bool
		input, event string // fixture commit names; an empty input is unset
		refused      bool
		stderr       string // the step's own refusal; empty when Git refuses first
	}{
		{"single child of the event commit", "release", "release", false, "", "request", false, ""},
		{"single child of the reviewed input commit", "release", "release", false, "request", "other", false, ""},
		// Current behaviour: the step does not require an annotated tag.
		{"lightweight tag", "release", "release", true, "", "request", false, ""},
		{"reviewed input naming another commit", "release", "release", false, "other", "request", true, refusal},
		{"child of another commit", "stray", "stray", false, "", "request", true, refusal},
		{"merge onto the request commit", "merge", "merge", false, "", "request", true, refusal},
		{"tag on another commit", "release", "other", false, "", "request", true, refusal},
		{"the request commit itself", "request", "request", false, "", "request", true, refusal},
		// Git refuses the missing tag before the step compares anything.
		{"missing tag", "release", "", false, "", "request", true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "prepare-release")
			commits := preparedReleaseFixture(t, box.root, test.tag, test.lightweight)

			got := box.run(script, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
				"FORGEJO_REF_NAME="+preparedReleaseTag, "SIGNED_RELEASE_SHA="+commits[test.signed],
				"RELEASE_REQUEST_SHA_INPUT="+commits[test.input], "RELEASE_REQUEST_EVENT_SHA="+commits[test.event])

			if !test.refused {
				if want := "release-sha=" + commits[test.signed] + "\n"; got.status != 0 || box.output() != want {
					t.Fatalf("status %d outputs %q, want %q; stderr %s", got.status, box.output(), want, got.stderr)
				}

				return
			}

			if got.status == 0 || box.output() != "" || (test.stderr != "" && got.stderr != test.stderr) {
				t.Fatalf("status %d outputs %q stderr %q, want a refusal with %q", got.status, box.output(), got.stderr, test.stderr)
			}
		})
	}
}

const rendererPins = ".forgejo/actions/render-changelog/versions.env"

// rendererPin reads one value of the action's renderer pins the way the step
// does, by sourcing the file.
func rendererPin(t *testing.T, name string) string {
	t.Helper()

	box := newSandbox(t, "prepare-release")
	pins := box.write("versions.env", string(reporoot.ReadFile(t, rendererPins)), 0o600)

	got := box.run("set -euo pipefail\nsource \"${PINS:?}\"\nprintf '%s' \"${!NAME:?}\"\n", "PINS="+pins, "NAME="+name)
	if got.status != 0 {
		t.Fatalf("%s pins no %s: %s", rendererPins, name, got.stderr)
	}

	return got.stdout
}

// The install step takes the selected renderer's version and both archive
// checksums from the pins beside the action and hands them to the engine. It
// passes no token, no tool-manager setting and no second renderer, and it
// stops before the engine on a backend the layer does not pin.
func TestPrepareReleaseInstallsThePinnedRenderer(t *testing.T) {
	t.Parallel()

	action := loadAction(t, "render-changelog")
	selected := action.step(t, "Install changelog renderer")
	script := action.script(t, "Install changelog renderer")

	if len(selected.Env) != 1 || selected.Env["CHANGELOG_BACKEND"] != "${{ inputs.changelog-backend }}" {
		t.Fatalf("the install step binds %v, want the backend input alone", selected.Env)
	}

	for _, name := range []string{"gh-rate-limit-token", "git-chglog-version", "git-cliff-version"} {
		if _, declared := action.Inputs[name]; declared {
			t.Errorf("the action declares the input %s: the layer pins the renderer, and its install reads no token", name)
		}
	}

	for backend, prefix := range map[string]string{"git-chglog": "GIT_CHGLOG", "git-cliff": "GIT_CLIFF"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()

			version := rendererPin(t, prefix+"_VERSION")
			x64, arm64 := rendererPin(t, prefix+"_LINUX_X64_SHA256"), rendererPin(t, prefix+"_LINUX_ARM64_SHA256")

			if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) ||
				!container.ValidSHA256Hex(x64) || !container.ValidSHA256Hex(arm64) || x64 == arm64 {
				t.Fatalf("%s pins %s as version %q with checksums %q and %q", rendererPins, backend, version, x64, arm64)
			}

			box := newSandbox(t, "render-changelog")
			box.write(rendererPins, string(reporoot.ReadFile(t, rendererPins)), 0o600)

			got := box.run(script, "CHANGELOG_BACKEND="+backend, "FORGEJO_PATH=/runner/path",
				"GITHUB_TOKEN=token-DO-NOT-FORWARD", "SSH_SIGNING_KEY=key-DO-NOT-FORWARD")

			want := []string{
				"toolchain", "install-changelog-renderer", "--backend", backend, "--version", version,
				"--linux-x64-sha256", x64, "--linux-arm64-sha256", arm64, "--path-file", "/runner/path",
			}
			if got.status != 0 || !slices.Equal(box.argv(), want) {
				t.Fatalf("status %d argv %q, want %q; stderr %s", got.status, box.argv(), want, got.stderr)
			}
		})
	}

	t.Run("refuses a backend the layer does not pin", func(t *testing.T) {
		t.Parallel()

		box := newSandbox(t, "render-changelog")
		box.write(rendererPins, string(reporoot.ReadFile(t, rendererPins)), 0o600)

		got := box.run(script, "CHANGELOG_BACKEND=aqua:other/tool", "FORGEJO_PATH=/runner/path")
		if got.status == 0 || box.argv() != nil || !strings.Contains(got.stderr, "changelog-backend must be git-chglog or git-cliff") {
			t.Fatalf("status %d argv %q; stderr %s", got.status, box.argv(), got.stderr)
		}
	})
}

// A verified version bump's files reach the engine as one more flag; an empty
// input adds none, so a release without a bump commits as before.
func TestPrepareReleaseSigningPassesTheVersionBumpPaths(t *testing.T) {
	t.Parallel()

	for _, paths := range []string{"", "/runner/temp/bump-paths"} {
		box := newSandbox(t, "prepare-release")
		signer := box.write(".forgejo/bin/signer", "#!/usr/bin/env bash\nprintf '%s\\0' \"$@\" >\"${FAKE_ARGV_LOG:?}\"\n", 0o700)
		got := box.run(loadAction(t, "prepare-release").script(t, "Sign changelog commit and create release tag"),
			"SIGNING_EXECUTABLE="+signer, "SSH_SIGNING_KEY=fixture-key", "FORGEJO_REF_NAME=v1.2.3", "FORGEJO_REPO=owner/repo",
			"TARGET_BRANCH=main", "RELEASE_GIT_URL=ssh://git@forgejo.invalid/owner/repo.git", "INCLUDE_PATHS_FILE="+paths)

		want := []string{"version", "commit-changelog-release", "--tag", "v1.2.3", "--branch", "main", "--repository", "owner/repo",
			"--git-url", "ssh://git@forgejo.invalid/owner/repo.git"}
		if paths != "" {
			want = append(want, "--include-paths-file", paths)
		}

		if got.status != 0 || !slices.Equal(box.argv(), want) {
			t.Fatalf("paths %q: status %d argv %q; stderr %s", paths, got.status, box.argv(), got.stderr)
		}
	}
}
