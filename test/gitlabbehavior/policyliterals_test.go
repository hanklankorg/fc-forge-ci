// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// policyTemplates are the components an independently controlled policy
// includes, whose values a consumer project's variables must not change.
var policyTemplates = []string{
	"templates/release.yml", "templates/release-jobs.yml", "templates/snapshot-images.yml",
	"templates/snapshot-image-jobs.yml", "templates/check.yml", "templates/nanolinter.yml", "templates/megalinter.yml",
}

// literalAssignment is a policy value assigned in a job's script from its
// input as written.
var literalAssignment = regexp.MustCompile(`\b([A-Z][A-Z0-9_]*)='\$\[\[ inputs\.([a-z0-9-]+) \]\]'`)

// A project, group or instance variable outranks a job's own and the
// predefined ones. Set as one, under its own name, each policy value is
// replaced by the literal input it is assigned from, or the runner setting
// the job depends on stops the job before it fetches anything.
func TestGitLabJobsTakeThePolicyFromTheirLiteralInputs(t *testing.T) {
	t.Parallel()

	pin := strings.Repeat("a", 40)
	kaniko := "registry.invalid/team/kaniko-job@sha256:" + strings.Repeat("9", 64)
	release := map[string]any{"job-user": "65532:65532", "engine-sha": pin, "engine-reference": pin}
	releaseEnv := []string{
		"FAKE_UID=65532", "FAKE_GID=65532", "RC_JOB_USER=65532:65532", "GIT_STRATEGY=empty", "GIT_SUBMODULE_STRATEGY=none",
		"CI_PROJECT_PATH=team/app", "CI_COMMIT_REF_PROTECTED=true", "CI_PIPELINE_SOURCE=parent_pipeline",
		"CI_COMMIT_TAG=release-request/v1.0.0",
	}
	snapshotEnv := []string{"GIT_STRATEGY=empty", "CI_PROJECT_PATH=team/app"}

	for _, tc := range []struct {
		name, path, job string
		script          func(t *testing.T) string
		given           map[string]any
		stop            string
		env             []string
		derived         map[string]string
		wantError       string
	}{
		{
			name: "release start", path: "templates/release-jobs.yml", script: releaseRequestStart,
			given: release, stop: "object_format=sha1", env: releaseEnv,
			derived: map[string]string{
				"FORGE_CI_REPOSITORY_URL": "https://gitlab.invalid/team/engine.git", "CI_API_V4_URL": "https://gitlab.invalid/api/v4",
				"CI_REGISTRY_IMAGE": "registry.gitlab.invalid/team/app",
			},
		},
		{
			name: "release start on another server", path: "templates/release-jobs.yml", script: releaseRequestStart,
			given: release, stop: "object_format=sha1", env: append(slices.Clone(releaseEnv), "CI_SERVER_URL=https://evil.invalid"),
			wantError: "CI_SERVER_URL is 'https://evil.invalid'",
		},
		{
			name: "release start on another registry", path: "templates/release-jobs.yml", script: releaseRequestStart,
			given: release, stop: "object_format=sha1", env: append(slices.Clone(releaseEnv), "CI_REGISTRY=evil.invalid"),
			wantError: "CI_REGISTRY 'evil.invalid'",
		},
		{
			name: "release start under another clone", path: "templates/release-jobs.yml", script: releaseRequestStart,
			given: release, stop: "object_format=sha1", env: append(slices.Clone(releaseEnv), "GIT_STRATEGY=fetch"),
			wantError: "GIT_STRATEGY is 'fetch'",
		},
		{
			name: "release start with submodules", path: "templates/release-jobs.yml", script: releaseRequestStart,
			given: release, stop: "object_format=sha1", env: append(slices.Clone(releaseEnv), "GIT_SUBMODULE_STRATEGY=recursive"),
			wantError: "GIT_SUBMODULE_STRATEGY 'recursive'",
		},
		{
			name: "release start with a variable signer tag", path: "templates/release-jobs.yml", script: releaseRequestStart,
			given: map[string]any{"job-user": "65532:65532", "engine-sha": pin, "engine-reference": pin, "signer-runner-tags": []any{"release", "$SIGNER_RUNNER"}},
			stop:  "object_format=sha1", env: releaseEnv, wantError: "signer-runner-tags names a $variable",
		},
		{
			name: "release start without amd64 build tags", path: "templates/release-jobs.yml", script: releaseRequestStart,
			given: map[string]any{"job-user": "65532:65532", "engine-sha": pin, "engine-reference": pin, "build-runner-tags-linux-amd64": []any{}},
			stop:  "object_format=sha1", env: releaseEnv, wantError: "build-runner-tags-linux-amd64 is empty",
		},
		{
			name: "snapshot start", path: "templates/snapshot-image-jobs.yml", script: snapshotStart,
			given: map[string]any{"image-builder": "kaniko", "builder-job-image": kaniko}, stop: `git init --quiet "${SNAPSHOT_HELPERS}"`,
			env: snapshotEnv, derived: map[string]string{
				"FORGE_CI_REPOSITORY_URL": "https://gitlab.invalid/team/engine.git", "SNAPSHOT_SERVER": "https://gitlab.invalid",
				"CI_API_V4_URL": "https://gitlab.invalid/api/v4",
			},
		},
		{
			name: "snapshot start on another server", path: "templates/snapshot-image-jobs.yml", script: snapshotStart,
			stop: `git init --quiet "${SNAPSHOT_HELPERS}"`, env: append(slices.Clone(snapshotEnv), "CI_SERVER_URL=https://evil.invalid"),
			wantError: "CI_SERVER_URL is 'https://evil.invalid'",
		},
		{
			name: "snapshot start on another registry", path: "templates/snapshot-image-jobs.yml", script: snapshotStart,
			stop: `git init --quiet "${SNAPSHOT_HELPERS}"`, env: append(slices.Clone(snapshotEnv), "CI_REGISTRY=evil.invalid"),
			wantError: "CI_REGISTRY 'evil.invalid'",
		},
		{
			name: "snapshot start under another clone", path: "templates/snapshot-image-jobs.yml", script: snapshotStart,
			stop: `git init --quiet "${SNAPSHOT_HELPERS}"`, env: append(slices.Clone(snapshotEnv), "GIT_STRATEGY=clone"),
			wantError: "GIT_STRATEGY is 'clone'",
		},
		{
			name: "snapshot plan", path: "templates/snapshot-images.yml", script: snapshotPlanPolicy,
			given: map[string]any{"build-runner-tags-linux-arm64": []any{"native-arm64"}},
		},
		{
			name: "check start", path: "templates/check.yml", script: checkStart, given: map[string]any{"job-user": ""},
			stop: "RC_HELPERS_DIR=$(mktemp", env: []string{"FAKE_UID=1000", "FAKE_GID=1000", "CI_COMMIT_SHA=" + pin, "RC_FORGE_CI_REPOSITORY=evil/engine"},
			derived: map[string]string{"FORGE_CI_REPOSITORY_URL": "https://gitlab.invalid/team/engine.git", "CI_API_V4_URL": "https://gitlab.invalid/api/v4"},
		},
		{
			name: "check start on another server", path: "templates/check.yml", script: checkStart, given: map[string]any{"job-user": ""},
			stop: "RC_HELPERS_DIR=$(mktemp", env: []string{"FAKE_UID=1000", "FAKE_GID=1000", "CI_COMMIT_SHA=" + pin, "CI_SERVER_URL=https://evil.invalid"},
			wantError: "CI_SERVER_URL is 'https://evil.invalid'",
		},
		{
			name: "nanolinter", path: "templates/nanolinter.yml", script: lintScript("templates/nanolinter.yml"), stop: "git_config_count=",
			derived: map[string]string{"CI_API_V4_URL": "https://gitlab.invalid/api/v4"},
		},
		{
			name: "nanolinter on another server", path: "templates/nanolinter.yml", script: lintScript("templates/nanolinter.yml"), stop: "git_config_count=",
			env: []string{"CI_SERVER_URL=https://evil.invalid"}, wantError: "CI_SERVER_URL is 'https://evil.invalid'",
		},
		{
			name: "megalinter", path: "templates/megalinter.yml", script: lintScript("templates/megalinter.yml"), stop: "git_config_count=",
			derived: map[string]string{"CI_API_V4_URL": "https://gitlab.invalid/api/v4"},
		},
		{
			name: "megalinter on another server", path: "templates/megalinter.yml", script: lintScript("templates/megalinter.yml"), stop: "git_config_count=",
			env: []string{"CI_SERVER_URL=https://evil.invalid"}, wantError: "CI_SERVER_URL is 'https://evil.invalid'",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			script := tc.script(t)
			if tc.stop != "" {
				before, _, found := strings.Cut(script, tc.stop)
				if !found {
					t.Fatalf("%s has no %q", tc.path, tc.stop)
				}

				script = before
			}

			want := map[string]string{}

			for _, match := range literalAssignment.FindAllStringSubmatch(script, -1) {
				// Quoted, as the script quotes it: an input alone would render
				// as its value, an array or a boolean.
				quoted := gitlabrender.Script(t, tc.path, "'$[[ inputs."+match[2]+" ]]'", tc.given)
				want[match[1]] = quoted[1 : len(quoted)-1]
			}

			if len(want) == 0 && len(tc.derived) == 0 {
				t.Fatalf("%s assigns no policy value from its inputs", tc.path)
			}

			maps.Copy(want, tc.derived)

			// Every value, as a project variable would set it.
			env := make([]string, 0, len(want)+3)

			env = append(env, "PATH=/usr/bin:/bin", "CI_SERVER_URL=https://gitlab.invalid", "CI_REGISTRY=registry.gitlab.invalid", "rc_job_role=sign")
			for name := range want {
				env = append(env, name+"=hostile-"+strings.ToLower(name))
			}

			names := make([]string, 0, len(want))
			for name := range want {
				names = append(names, name)
			}

			slices.Sort(names)

			probe := "\nfor name in " + strings.Join(names, " ") + `; do printf 'policy %s=%s\n' "${name}" "${!name}"; done` +
				"\nprintf 'exported %s\\n' \"$(printenv FORGE_CI_COSIGN_TRANSPARENCY || :)\"\nexit 3\n"
			output, err := runPolicyScript(t, fakeID+gitlabrender.Script(t, tc.path, script, tc.given)+probe, append(env, tc.env...))

			if tc.wantError != "" {
				if err == nil || !strings.Contains(output, "ERROR: ") || !strings.Contains(output, tc.wantError) || strings.Contains(output, "policy ") {
					t.Fatalf("want the refusal %q before the policy:\n%s", tc.wantError, output)
				}

				return
			}

			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 3 {
				t.Fatalf("the script did not reach its probe: %v\n%s", err, output)
			}

			for _, name := range names {
				if line := fmt.Sprintf("policy %s=%s\n", name, want[name]); !strings.Contains(output, line) {
					t.Errorf("%s is not its literal %q:\n%s", name, want[name], output)
				}
			}

			// The engine reads the transparency setting from its environment.
			if value, ok := want["FORGE_CI_COSIGN_TRANSPARENCY"]; ok && !strings.Contains(output, "exported "+value+"\n") {
				t.Errorf("FORGE_CI_COSIGN_TRANSPARENCY does not reach the engine as %q:\n%s", value, output)
			}
		})
	}
}

func runPolicyScript(t *testing.T, script string, env []string) (string, error) {
	t.Helper()

	dir := t.TempDir()

	ctx := commandContext(t)

	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c", script)
	command.Dir = dir
	command.Env = append([]string{"HOME=" + dir, "CI_PROJECT_DIR=" + dir, "TMPDIR=" + dir}, env...)

	output, err := command.CombinedOutput()

	return string(output), err
}

func releaseRequestStart(t *testing.T) string {
	t.Helper()

	return releaseJobStart(t, "Check: release request", "kaniko")
}

func snapshotStart(t *testing.T) string {
	t.Helper()

	return joinedScript(releaseDocumentBody(t, "templates/snapshot-image-jobs.yml")[".forge-ci-snapshot"].(map[string]any)["before_script"])
}

func snapshotPlanPolicy(t *testing.T) string {
	t.Helper()

	return yamlStrings(releaseDocumentBody(t, "templates/snapshot-images.yml")["Snapshot: plan"].(map[string]any)["script"])[0]
}

// checkStart is the check job's start pinned by its full commit.
func checkStart(t *testing.T) string {
	t.Helper()

	return strings.ReplaceAll(checkBootstrapScript(t), "$[[ component.reference ]]", "$[[ component.sha ]]")
}

func lintScript(path string) func(t *testing.T) string {
	return func(t *testing.T) string {
		t.Helper()

		return yamlStrings(releaseDocumentBody(t, path)["$[[ inputs.job-name ]]"].(map[string]any)["script"])[0]
	}
}

// heldVariables are the job variables that carry an input: values a tool
// reads from the environment before the job's shell, each held by the job's
// start to its input, or an input that names a variable for the runner to
// expand.
var heldVariables = map[string]string{
	"RC_JOB_USER":            "the runner's image user; the start holds it to job-user",
	"RC_FORGE_CI_REPOSITORY": "the lint components' engine project, which may name $CI_PROJECT_PATH; the component's commit and the engine's recorded checksum bind what runs",
}

// No job variable of a policy component carries one of its inputs but the
// held ones: a project, group or instance variable outranks the job's own.
func TestGitLabPolicyComponentsKeepTheirInputsOutOfJobVariables(t *testing.T) {
	t.Parallel()

	for _, path := range policyTemplates {
		for job, raw := range releaseDocumentBody(t, path) {
			fields, isJob := raw.(map[string]any)
			if !isJob {
				continue
			}

			variables, _ := fields["variables"].(map[string]any)
			for name, value := range variables {
				text, _ := value.(string)
				if !strings.Contains(text, "$[[ inputs.") {
					continue
				}

				if _, held := heldVariables[name]; !held || name == "RC_FORGE_CI_REPOSITORY" && !strings.Contains(path, "linter") {
					t.Errorf("%s: %s takes %s from a job variable; assign it in the script from the input", path, job, name)
				}
			}
		}
	}
}

// policyName is a name of the policy's values: the release's RC_*, the
// snapshot's SNAPSHOT_*, the image builder and the engine's settings.
var policyName = regexp.MustCompile(`\$\{?!?(RC_[A-Z0-9_]+|SNAPSHOT_[A-Z0-9_]+|CI_IMAGE_BUILDER[A-Z_]*|FORGE_CI_COSIGN_TRANSPARENCY|REQUIRED_LINTS|ENABLE_GITLAB_SAST)\b`)

// derivedPolicyNames are the policy-named values the jobs read that are not
// assigned from an input, and where they come from instead.
var derivedPolicyNames = map[string]string{
	"RC_JOB_USER":                "held to job-user by the start",
	"RC_WORK":                    "the start's private work directory",
	"RC_HELPERS_DIR":             "the start's checkout of the engine",
	"RC_RELEASE_BRANCH":          "rc_release_context, from release-branch or the default branch",
	"RC_FORGE_CI_REPOSITORY":     "the lint components' held variable",
	"SNAPSHOT_HELPERS":           "the snapshot start's checkout of the engine",
	"SNAPSHOT_SERVER":            "the snapshot start, from server-url",
	"RC_REGISTRY_IMAGE":          "the release start, from registry and consumer-project, as GitLab derives CI_REGISTRY_IMAGE",
	"SNAPSHOT_REPOSITORY":        "the snapshot start, from CI_PROJECT_PATH",
	"SNAPSHOT_REGISTRY_USER":     "a credential of the snapshot/registry environment",
	"SNAPSHOT_REGISTRY_PASSWORD": "a credential of the snapshot/registry environment",
}

// What the GitLab jobs read under a policy value's name is a literal of a
// policy component's script, or one of the derived values: never only a
// variable, which a project can set.
func TestGitLabScriptsReadPolicyValuesOnlyFromLiterals(t *testing.T) {
	t.Parallel()

	literal := map[string]bool{}

	texts := make([]string, 0, len(policyTemplates)+3)

	for _, path := range policyTemplates {
		text := string(reporoot.ReadFile(t, path))
		for _, match := range literalAssignment.FindAllStringSubmatch(text, -1) {
			literal[match[1]] = true
		}

		texts = append(texts, path+"\x00"+text)
	}

	for _, path := range []string{".gitlab/bin/release.sh", ".gitlab/bin/snapshot-images.sh", ".forgejo/bin/snapshot-images.sh"} {
		texts = append(texts, path+"\x00"+string(reporoot.ReadFile(t, path)))
	}

	for _, entry := range texts {
		path, text, _ := strings.Cut(entry, "\x00")
		for _, match := range policyName.FindAllStringSubmatch(text, -1) {
			if _, derived := derivedPolicyNames[match[1]]; !literal[match[1]] && !derived {
				t.Errorf("%s reads %s, which no policy component assigns from an input", path, match[1])
			}
		}
	}
}

// serverVariable is a predefined variable naming where a job's tokens go: the
// instance, its API or its registry. A project or pipeline variable outranks
// each of them.
var serverVariable = regexp.MustCompile(`\$\{?!?(CI_SERVER_URL|CI_API_V4_URL|CI_API_GRAPHQL_URL|CI_REGISTRY|CI_REGISTRY_IMAGE|CI_SERVER_HOST|CI_SERVER_FQDN|CI_SERVER_PORT|CI_SERVER_PROTOCOL)\b`)

// serverLiteral is the regex of a policy component's server-url input.
const serverLiteral = `^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`

// Every policy component takes the instance as a literal, server-url, that
// admits exactly an https origin, and its jobs send their tokens only where
// the literal points: a script line that reads a predefined server
// variable compares it with, or derives it from, the literal. The release
// and snapshot components take the registry the same way, since GitLab
// configures it apart from the web host.
func TestGitLabTokenDestinationsComeFromTheServerLiteral(t *testing.T) {
	t.Parallel()

	admitted := []string{"https://gitlab.example.com", "https://gitlab.example.com:8443", "https://10.0.0.1"}
	refused := []string{
		"", "http://gitlab.example.com", "https://gitlab.example.com/", "https://gitlab.example.com/gitlab",
		"https://user:pass@gitlab.example.com", "https://gitlab.example.com?x=1", "https://gitlab.example.com#x",
		"https://gitlab.example.com\nhttps://evil.invalid", "$CI_SERVER_URL", "https://-evil.invalid",
	}

	for _, path := range policyTemplates {
		spec := gitlabrender.InputSpec(t, path)

		input, declared := spec["server-url"]
		if pattern, _ := input["regex"].(string); !declared || pattern != serverLiteral {
			t.Errorf("%s: server-url must be declared with regex %s, got %v", path, serverLiteral, input["regex"])

			continue
		}

		admits := regexp.MustCompile(serverLiteral).MatchString
		for _, value := range admitted {
			if !admits(value) {
				t.Errorf("server-url refuses %q", value)
			}
		}

		for _, value := range refused {
			if admits(value) {
				t.Errorf("server-url admits %q", value)
			}
		}

		if _, hasDefault := input["default"]; hasDefault {
			t.Errorf("%s: server-url has a default; the administrator states it", path)
		}

		if strings.Contains(path, "release") || strings.Contains(path, "snapshot") {
			registry, ok := spec["registry"]
			if _, hasDefault := registry["default"]; !ok || hasDefault {
				t.Errorf("%s: registry must be a required literal input", path)
			}
		}
	}

	texts := make([]string, 0, len(policyTemplates)+2)
	for _, path := range policyTemplates {
		_, body, _ := strings.Cut(string(reporoot.ReadFile(t, path)), "\n---\n")
		texts = append(texts, path+"\x00"+body)
	}

	// GitLab sources shared Forgejo step helpers too. Inventory both owners,
	// rather than checking only the two current GitLab entry-point scripts.
	for _, dir := range []string{".gitlab/bin", ".forgejo/bin"} {
		entries, err := os.ReadDir(filepath.Join(reporoot.Path(t), dir))
		if err != nil {
			t.Fatal(err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sh") {
				continue
			}

			path := dir + "/" + entry.Name()
			texts = append(texts, path+"\x00"+string(reporoot.ReadFile(t, path)))
		}
	}

	held := 0

	for _, entry := range texts {
		path, text, _ := strings.Cut(entry, "\x00")
		for number, line := range strings.Split(text, "\n") {
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "#") || !serverVariable.MatchString(line) {
				continue
			}

			if !strings.Contains(line, "${RC_SERVER_URL}") && !strings.Contains(line, "${RC_REGISTRY}") {
				t.Errorf("%s:%d reads %s; take the instance from the server-url or registry literal: %s",
					path, number+1, serverVariable.FindStringSubmatch(line)[1], strings.TrimSpace(line))

				continue
			}

			held++
		}
	}

	if held == 0 {
		t.Fatal("no job holds a server variable to its literal; the guard has drifted")
	}

	for _, path := range []string{"templates/release-jobs.yml", "templates/snapshot-image-jobs.yml", "templates/check.yml", "templates/nanolinter.yml", "templates/megalinter.yml"} {
		text := string(reporoot.ReadFile(t, path))
		if !strings.Contains(text, `RC_SERVER_URL='$[[ inputs.server-url ]]'`) || !strings.Contains(text, `"${CI_SERVER_URL-}" != "${RC_SERVER_URL}"`) ||
			!strings.Contains(text, `CI_API_V4_URL="${RC_SERVER_URL}/api/v4"`) {
			t.Errorf("%s: the job start must assign RC_SERVER_URL from server-url, hold CI_SERVER_URL to it and derive CI_API_V4_URL", path)
		}
	}
}
