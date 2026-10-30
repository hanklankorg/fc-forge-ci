// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

type releaseRoutingJob struct {
	Extends     string
	Stage       string
	Tags        string
	Needs       []any
	Script      []string
	Environment any
	Rules       []any
	When        string
}

func releaseRouting(t *testing.T, path string) (map[string]any, map[string]releaseRoutingJob) {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(reporoot.ReadFile(t, path)))
	var header map[string]any
	if err := decoder.Decode(&header); err != nil {
		t.Fatal(err)
	}
	var jobs map[string]releaseRoutingJob
	// stages is not a job; decode the document's job mappings individually.
	var body map[string]yaml.Node
	if err := decoder.Decode(&body); err != nil {
		t.Fatal(err)
	}
	jobs = make(map[string]releaseRoutingJob)
	for name, node := range body {
		if node.Kind == yaml.MappingNode {
			var job releaseRoutingJob
			if err := node.Decode(&job); err != nil {
				t.Fatal(err)
			}
			jobs[name] = job
		}
	}
	return header, jobs
}

func TestReleaseChecksRoutingAndPreparationBarrier(t *testing.T) {
	t.Parallel()
	_, jobs := releaseRouting(t, "templates/release-jobs.yml")
	auth, checks := jobs["Check: release request"], jobs["Check: consumer checks"]
	if auth.Tags != "" || !reflect.DeepEqual(auth.Script, []string{"rc_request_checkout"}) {
		t.Fatalf("authorization must stay restricted and execute no consumer commands: %+v", auth)
	}
	if !reflect.DeepEqual(auth.Needs, []any{"Check: signing access", "Check: publishing access", "Check: registry access"}) {
		t.Fatal("authorization bypasses access preflights")
	}
	if checks.Extends != ".forge-ci-release" || checks.Stage != "check" || checks.Tags != "$[[ inputs.checks-runner-tags ]]" ||
		!reflect.DeepEqual(checks.Needs, []any{map[string]any{"job": "Check: release request", "artifacts": false}}) ||
		!reflect.DeepEqual(checks.Script, []string{"rc_request_checkout", "$[[ inputs.checks ]]"}) || checks.Environment != nil || len(checks.Rules) != 0 || checks.When != "" {
		t.Fatalf("consumer checks lost authorization, fresh revalidation, or secret isolation: %+v", checks)
	}
	for _, name := range []string{"Prepare: changelog", "Prepare: version"} {
		if !reflect.DeepEqual(jobs[name].Needs, []any{"Check: consumer checks"}) {
			t.Fatalf("%s can bypass successful checks: %+v", name, jobs[name])
		}
	}
	prepare := jobs["Prepare: release commit and tag"]
	if !reflect.DeepEqual(prepare.Needs, []any{
		map[string]any{"job": "Prepare: changelog", "artifacts": true},
		map[string]any{"job": "Prepare: version", "artifacts": true, "optional": true},
	}) {
		t.Fatalf("release preparation lost required changelog or optional version barrier: %+v", prepare)
	}
	if !reflect.DeepEqual(jobs["Prepare: version"].Rules, []any{map[string]any{"if": `"$[[ inputs.version-bump ]]" != ""`}}) {
		t.Fatal("version bump must remain optional without weakening the checks barrier")
	}
	if !reflect.DeepEqual(jobs["Build: release files"].Needs, []any{map[string]any{"job": "Prepare: release commit and tag", "artifacts": false}}) {
		t.Fatal("build bypasses gated release preparation")
	}
	for name, job := range jobs {
		if name != "Check: consumer checks" && name != "Build: release files" && job.Tags != "" {
			t.Errorf("unrelated job %s acquired stronger scheduling: %s", name, job.Tags)
		}
	}
	for _, path := range []string{"templates/release.yml", "templates/release-jobs.yml"} {
		header, _ := releaseRouting(t, path)
		input := header["spec"].(map[string]any)["inputs"].(map[string]any)["checks-runner-tags"].(map[string]any)
		if input["type"] != "array" || !reflect.DeepEqual(input["default"], []any{}) {
			t.Fatalf("%s changed the ordinary-consumer default: %+v", path, input)
		}
	}
	// Check the real trigger mapping, not a duplicate rendered fixture.
	decoder := yaml.NewDecoder(bytes.NewReader(reporoot.ReadFile(t, "templates/release.yml")))
	var ignored, root map[string]any
	if err := decoder.Decode(&ignored); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	trigger := root["release"].(map[string]any)["trigger"].(map[string]any)
	include := trigger["include"].([]any)[0].(map[string]any)
	if include["inputs"].(map[string]any)["checks-runner-tags"] != "$[[ inputs.checks-runner-tags ]]" {
		t.Fatal("root component does not forward checks runner tags")
	}
}

func TestReleaseSummaryIncludesChecksAndOnlyEnabledVersionBump(t *testing.T) {
	t.Parallel()
	for _, bump := range []string{"", "cargo"} {
		t.Run("bump="+bump, func(t *testing.T) {
			t.Parallel()
			command := exec.CommandContext(t.Context(), "/bin/bash", "--noprofile", "--norc", "-eu", "-c",
				`source "$1"; rc_engine() { printf '%s\n' "$@"; }; rc_release_summary`, "summary",
				filepath.Join(reporoot.Path(t), ".gitlab/bin/release.sh"))
			command.Env = []string{"PATH=/usr/bin:/bin", "RC_VERSION_BUMP=" + bump, "RELEASE_TAG=v1.2.3", "RC_RELEASE_BRANCH=main"}
			out, err := command.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "Check: consumer checks=success") ||
				strings.Contains(string(out), "Prepare: version=success") != (bump != "") {
				t.Fatalf("summary misrepresents checks/version jobs: %v\n%s", err, out)
			}
		})
	}
}

// Execute the actual job script and checkout driver with an inert engine. A
// rejection at any admission step must prevent the consumer command entirely.
func TestConsumerChecksRevalidateImmutableRequestBeforeCommands(t *testing.T) {
	t.Parallel()
	_, jobs := releaseRouting(t, "templates/release-jobs.yml")
	jobScript := strings.ReplaceAll(strings.Join(jobs["Check: consumer checks"].Script, "\n"), "$[[ inputs.checks ]]", "echo consumer-command")
	for _, failure := range []string{"checkout", "signature", "head", "none"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			command := exec.CommandContext(t.Context(), "/bin/bash", "--noprofile", "--norc", "-eu", "-c", `source "$1"
rc_engine() {
  printf 'engine %s\n' "$*"
  case "$1 $2" in
    'platform checkout') test "$FAILURE" != checkout; mkdir -p "$RC_WORK/consumer" ;;
    'release validate-request') test "$FAILURE" != signature ;;
    'validate tag') test "$FAILURE" != head ;;
  esac
}
`+jobScript, "checks", filepath.Join(reporoot.Path(t), ".gitlab/bin/release.sh"))
			sha := strings.Repeat("a", 40)
			command.Env = []string{"PATH=/usr/bin:/bin", "RC_WORK=" + dir, "FAILURE=" + failure,
				"RELEASE_REQUEST_SHA=" + sha, "RC_REQUEST_ALLOWED_SIGNERS=fixture signer", "CI_COMMIT_TAG=release-request/v1.2.3",
				"RELEASE_TAG=v1.2.3", "RC_RELEASE_BRANCH=main", "CI_PROJECT_PATH=owner/project", "CI_SERVER_URL=https://gitlab.invalid"}
			out, err := command.CombinedOutput()
			if (err == nil) != (failure == "none") || strings.Contains(string(out), "consumer-command") != (failure == "none") {
				t.Fatalf("admission %s: %v\n%s", failure, err, out)
			}
			if failure == "none" {
				for _, want := range []string{"--workspace " + dir + "/consumer", "--ref " + sha, "--expected-commit " + sha, "--tag release-request/v1.2.3 --branch main --require-head"} {
					if !strings.Contains(string(out), want) {
						t.Errorf("missing immutable admission %q in %s", want, out)
					}
				}
			}
		})
	}
}
