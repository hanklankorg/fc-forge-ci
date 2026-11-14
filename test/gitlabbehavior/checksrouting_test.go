// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

type releaseRoutingJob struct {
	Extends     any
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
	if auth.Tags != "" || !reflect.DeepEqual(auth.Script, []string{"rc_request_checkout", releaseImagesCheck, exportsCheck}) {
		t.Fatalf("authorization must stay restricted and execute no consumer commands: %+v", auth)
	}

	if !reflect.DeepEqual(auth.Needs, []any{"Check: signing access", "Check: publishing access", map[string]any{"job": "Check: registry access", "optional": true}}) {
		t.Fatal("authorization bypasses access preflights")
	}

	if checks.Extends != ".forge-ci-release" || checks.Stage != "check" || checks.Tags != "$[[ inputs.build-runner-tags-linux-amd64 ]]" ||
		!reflect.DeepEqual(checks.Needs, []any{map[string]any{"job": "Check: release request", "artifacts": false}}) ||
		!reflect.DeepEqual(checks.Script, []string{"rc_request_checkout", "$[[ inputs.checks ]]"}) || checks.Environment != nil || len(checks.Rules) != 0 || checks.When != "" {
		t.Fatalf("consumer checks lost authorization, fresh revalidation, or secret isolation: %+v", checks)
	}

	// The changelog runs beside the checks once the request is authorized;
	// the release commit waits for both.
	if !reflect.DeepEqual(jobs["Prepare: changelog"].Needs, []any{map[string]any{"job": "Check: release request", "artifacts": false}}) {
		t.Fatalf("the changelog can bypass authorization: %+v", jobs["Prepare: changelog"])
	}

	// The plan's version jobs, whichever it names, wait for the checks and
	// carry no rule of their own: "Release: plan" decides that they exist.
	version := jobs[".forge-ci-release-version"]
	if !reflect.DeepEqual(version.Needs, []any{
		map[string]any{"job": "Check: consumer checks", "artifacts": false},
		map[string]any{"job": "Prepare: changelog", "artifacts": true},
	}) || len(version.Rules) != 0 {
		t.Fatalf("a version job can bypass successful checks: %+v", version)
	}

	families := map[string][]string{"version": {"java", "node", "rust"}, "build": {"java", "node", "go", "rust", "android"}}

	prepareNeeds := make([]any, 0, 2+len(families["version"]))
	prepareNeeds = append(prepareNeeds,
		map[string]any{"job": "Check: consumer checks", "artifacts": false},
		map[string]any{"job": "Prepare: changelog", "artifacts": true})

	for _, family := range families["version"] {
		prepareNeeds = append(prepareNeeds, map[string]any{"job": "Prepare: version " + family, "artifacts": true, "optional": true})
	}

	if prepare := jobs["Prepare: release commit and tag"]; !reflect.DeepEqual(prepare.Needs, prepareNeeds) {
		t.Fatalf("release preparation lost the checks, changelog or optional version barrier: %+v", prepare)
	}

	if family := jobs[".forge-ci-release-build-family"]; !reflect.DeepEqual(family.Needs, []any{
		map[string]any{"job": "Prepare: release commit and tag", "artifacts": false},
	}) || family.Tags != "$[[ inputs.build-runner-tags-linux-amd64 ]]" || len(family.Rules) != 0 {
		t.Fatalf("a family build bypasses gated release preparation: %+v", family)
	}

	buildNeeds := make([]any, 0, 3+len(families["build"]))
	buildNeeds = append(buildNeeds,
		map[string]any{"job": "Prepare: release commit and tag", "artifacts": false},
		map[string]any{"job": "Build: export amd64", "artifacts": true, "optional": true},
		map[string]any{"job": "Build: export arm64", "artifacts": true, "optional": true},
	)

	for _, family := range families["build"] {
		buildNeeds = append(buildNeeds, map[string]any{"job": "Build: " + family, "artifacts": true, "optional": true})
	}

	// With base stages, the files wait for every base's promotion.
	buildNeeds = append(buildNeeds, map[string]any{"job": "Base: all promoted", "artifacts": true, "optional": true})

	if !reflect.DeepEqual(jobs["Build: release files"].Needs, buildNeeds) {
		t.Fatal("build bypasses gated release preparation or a family build")
	}

	for _, arch := range []string{"amd64", "arm64"} {
		if !reflect.DeepEqual(jobs["Build: export "+arch].Needs, []any{map[string]any{"job": "Prepare: release commit and tag", "artifacts": false}}) {
			t.Fatalf("the %s export bypasses gated release preparation", arch)
		}
	}

	// Every job runs on the policy's runner-tags and the signer jobs on its
	// signer-runner-tags. Consumer checks and build commands, plus the image
	// and export jobs of either builder, name their platform's build runner.
	scheduled := map[string]string{
		".forge-ci-release":        "$[[ inputs.runner-tags ]]",
		".forge-ci-release-signer": "$[[ inputs.signer-runner-tags ]]",
		// The package job holds a release credential in the node image.
		".forge-ci-release-publish-npm-packages": "$[[ inputs.signer-runner-tags ]]",
		// A base stage builds on the build runner; its publishing job holds
		// the registry credential in job-image.
		".forge-ci-release-base-build":     "$[[ inputs.build-runner-tags-linux-amd64 ]]",
		".forge-ci-release-base-publish":   "$[[ inputs.signer-runner-tags ]]",
		"Check: consumer checks":           "$[[ inputs.build-runner-tags-linux-amd64 ]]",
		"Build: release files":             "$[[ inputs.build-runner-tags-linux-amd64 ]]",
		".forge-ci-release-build-family":   "$[[ inputs.build-runner-tags-linux-amd64 ]]",
		".forge-ci-release-image-builder":  "$[[ inputs.build-runner-tags-linux-amd64 ]]",
		"Images: build arm64":              "$[[ inputs.build-runner-tags-linux-arm64 ]]",
		".forge-ci-release-export-builder": "$[[ inputs.build-runner-tags-linux-amd64 ]]",
		"Build: export arm64":              "$[[ inputs.build-runner-tags-linux-arm64 ]]",
	}
	for name, job := range jobs {
		if job.Tags != scheduled[name] {
			t.Errorf("job %s has scheduling %q, want %q", name, job.Tags, scheduled[name])
		}
	}

	// The unbound inputs default to untagged; the platform-bound build
	// inputs have no default, since an untagged job could land anywhere.
	for _, path := range []string{"templates/release.yml", "templates/release-jobs.yml"} {
		header, _ := releaseRouting(t, path)

		for _, name := range []string{"runner-tags", "signer-runner-tags", "build-runner-tags-linux-amd64", "build-runner-tags-linux-arm64"} {
			input := header["spec"].(map[string]any)["inputs"].(map[string]any)[name].(map[string]any)
			defaulted, hasDefault := input["default"]

			want := reflect.DeepEqual(defaulted, []any{})
			if strings.Contains(name, "-linux-") {
				want = !hasDefault
			}

			if input["type"] != "array" || !want {
				t.Fatalf("%s changed the default of %s: %+v", path, name, input)
			}
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
	for _, name := range []string{"runner-tags", "signer-runner-tags", "build-runner-tags-linux-amd64", "build-runner-tags-linux-arm64"} {
		if include["inputs"].(map[string]any)[name] != "$[[ inputs."+name+" ]]" {
			t.Fatalf("root component does not forward %s", name)
		}
	}
}

// The summary lists the family jobs the request's plan names, and no other;
// it plans again rather than downloading the jobs' files.
func TestReleaseSummaryIncludesChecksAndOnlyThePlannedFamilyJobs(t *testing.T) {
	t.Parallel()

	for _, planned := range [][]string{nil, {"Prepare: version rust", "Build: java", "Build: node", "Base: plan base", "Base: all promoted"}} {
		t.Run(strings.Join(planned, ","), func(t *testing.T) {
			t.Parallel()

			command := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-eu", "-c",
				`source "$1"; rc_engine() { printf '%s\n' "$@"; }; rc_checkout() { printf 'checkout %s\n' "$1"; }
rc_planned_jobs() { printf '%s' "${FAKE_PLANNED}" >"$1"; }; rc_release_summary`, "summary",
				filepath.Join(reporoot.Path(t), ".gitlab/bin/release.sh"))

			jobs := ""
			if len(planned) > 0 {
				jobs = strings.Join(planned, "\n") + "\n"
			}

			command.Env = []string{"PATH=/usr/bin:/bin", "RELEASE_TAG=v1.2.3", "RC_RELEASE_BRANCH=main", "RELEASE_REQUEST_SHA=" + strings.Repeat("a", 40),
				"RC_WORK=" + t.TempDir(), "FAKE_PLANNED=" + jobs, "RC_BASE_STAGES=" + map[bool]string{true: "base"}[len(planned) > 0]}

			out, err := command.CombinedOutput()
			// The plan's base jobs are listed with it, and the registry check they need.
			bases := len(planned) > 0
			if strings.Contains(string(out), "Base: plan base=success") != bases || strings.Contains(string(out), "Base: all promoted=success") != bases ||
				strings.Contains(string(out), "Check: registry access=success") != bases {
				t.Fatalf("summary misrepresents the base stage: %v\n%s", err, out)
			}

			if err != nil || !strings.Contains(string(out), "Check: consumer checks=success") || !strings.Contains(string(out), "Build: release files=success") {
				t.Fatalf("summary misrepresents the checks or the release files: %v\n%s", err, out)
			}

			if !strings.Contains(string(out), "checkout "+strings.Repeat("a", 40)) {
				t.Errorf("summary did not plan the request:\n%s", out)
			}

			for _, job := range []string{"Prepare: version rust", "Prepare: version java", "Build: java", "Build: node", "Build: go"} {
				if strings.Contains(string(out), job+"=success") != slices.Contains(planned, job) {
					t.Errorf("summary misrepresents %s:\n%s", job, out)
				}
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
			command := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-eu", "-c", `source "$1"
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
				"RELEASE_TAG=v1.2.3", "RC_RELEASE_BRANCH=main", "CI_PROJECT_PATH=owner/project", "RC_SERVER_URL=https://gitlab.invalid"}

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
