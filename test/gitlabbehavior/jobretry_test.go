// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// componentJob is the one job of a single-job component.
var componentJob = []string{"$[[ inputs.job-name ]]"}

// retriedJobs are the jobs and hidden jobs of each policy template that name
// the retry: those that read or build and hand on files.
var retriedJobs = map[string][]string{
	"templates/release.yml": {"Release: plan"},
	"templates/release-jobs.yml": {
		"Check: signing access", "Check: publishing access", "Check: registry access",
		"Check: release request", "Check: consumer checks", "Prepare: changelog",
		".forge-ci-release-version", ".forge-ci-release-build-family", "Build: release files",
		".forge-ci-release-base-plan", ".forge-ci-release-base-parent", ".forge-ci-release-base-build",
		".forge-ci-release-base-gate", ".forge-ci-release-exports", "Build: export amd64", "Build: export arm64",
		"Images: plan", "Images: build amd64", "Images: build arm64", "Summary: release",
	},
	"templates/snapshot-images.yml": {"Snapshot: plan"},
	// The single-job components: the check, the lints and the builds.
	"templates/check.yml": componentJob, "templates/nanolinter.yml": componentJob, "templates/megalinter.yml": componentJob,
	"templates/build-cargo.yml": componentJob, "templates/build-go.yml": componentJob, "templates/build-gradle.yml": componentJob,
	"templates/build-gradle-android.yml": componentJob, "templates/build-maven.yml": componentJob, "templates/build-npm.yml": componentJob,
	"templates/snapshot-image-jobs.yml": {
		".forge-ci-snapshot-produce", ".forge-ci-snapshot-context", ".forge-ci-snapshot-build", ".forge-ci-snapshot-collect",
		".forge-ci-snapshot-files",
	},
}

// neverRetriedJobs push an image, sign, tag, move a tag, publish the release
// or a package, or delete, so a second attempt could repeat what the first
// did. Each says retry: 0 itself: a job's own key outranks what it extends
// and the default of the configuration that includes the component. The
// triggers that start the pipelines of such jobs name none: GitLab refuses
// the key on a trigger job, and the configuration with it.
var neverRetriedJobs = map[string][]string{
	"templates/release-jobs.yml": {
		".forge-ci-release", "Prepare: release commit and tag", ".forge-ci-release-base-publish", ".forge-ci-release-base-promote",
		"Images: publish", "Sign: files and images", "Publish: images", "Publish: release", "Publish: tidy up",
		".forge-ci-release-publish-npm-packages",
	},
	// The three publisher components never run twice.
	"templates/publish-forge-packages.yml": componentJob, "templates/publish-maven-central.yml": componentJob,
	"templates/publish-apple-appstore.yml": componentJob,
	"templates/snapshot-image-jobs.yml":    {".forge-ci-snapshot", ".forge-ci-snapshot-publish"},
}

// templateJobs is every job and hidden job of a component as it is written.
func templateJobs(t *testing.T, path string) map[string]map[string]any {
	t.Helper()

	_, body, found := strings.Cut(string(reporoot.ReadFile(t, path)), "\n---\n")
	if !found {
		t.Fatalf("%s: not a component", path)
	}

	var keys map[string]any
	if err := yaml.Unmarshal([]byte(body), &keys); err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	jobs := map[string]map[string]any{}

	for name, value := range keys {
		if job, isJob := value.(map[string]any); isJob {
			jobs[name] = job
		}
	}

	return jobs
}

// A policy job is run again by GitLab at most once, and only when the engine
// ended it because a dependency was unavailable. The retry is a literal of
// the template, on the jobs listed and no other: a job that pushes, signs,
// tags, promotes, publishes or deletes says retry: 0, fails once and is a
// person's to run again.
func TestGitLabPolicyJobsRetryOnceOnAnUnavailableDependencyOnly(t *testing.T) {
	t.Parallel()

	want := map[string]any{"max": 1, "exit_codes": int(errs.ExitCodeUnavailable)}

	templates, err := filepath.Glob(filepath.Join(reporoot.Path(t), "templates", "*.yml"))
	if err != nil || len(templates) == 0 {
		t.Fatalf("no component found: %v", err)
	}

	for _, file := range templates {
		path := "templates/" + filepath.Base(file)
		jobs := templateJobs(t, path)

		var retried, never []string

		for name, job := range jobs {
			own, named := job["retry"]

			if _, trigger := job["trigger"]; trigger {
				if named {
					t.Errorf("%s: trigger job %s names a retry, which GitLab refuses", path, name)
				}

				continue
			}

			switch retry := resolvedRetry(jobs, name); {
			case named && reflect.DeepEqual(own, want):
				retried = append(retried, name)
			case named && own == 0:
				never = append(never, name)
			case named:
				t.Errorf("%s: job %s retries with %v, want exactly %v or 0", path, name, own, want)
			case strings.HasPrefix(name, "."):
				// A hidden job is a part of others; one a generated job
				// extends is listed and names its retry above.
			default:
				// A job that runs names its retry itself and is listed
				// here, so a new one is a decision: the including
				// configuration's default reaches no job.
				t.Errorf("%s: job %s names no retry (it would take %v)", path, name, retry)
			}
		}

		for got, listed := range map[*[]string][]string{&retried: retriedJobs[path], &never: neverRetriedJobs[path]} {
			slices.Sort(*got)

			if sorted := slices.Sorted(slices.Values(listed)); !slices.Equal(*got, sorted) {
				t.Errorf("%s: the jobs naming this retry are\n  %q\nwant\n  %q", path, *got, sorted)
			}
		}

		// No job leaves the retry to the pipeline's default, which the
		// including configuration could set.
		if _, named := jobs["default"]; named {
			t.Errorf("%s sets a pipeline default", path)
		}
	}
}

// resolvedRetry is the retry a job names or takes from what it extends,
// and nil when neither does. A parent chosen by an input is every hidden
// job its name can become.
func resolvedRetry(jobs map[string]map[string]any, name string) any {
	if retry, named := jobs[name]["retry"]; named {
		return retry
	}

	var parents []string

	switch extends := jobs[name]["extends"].(type) {
	case string:
		parents = []string{extends}
	case []any:
		for _, parent := range extends {
			parents = append(parents, parent.(string))
		}
	}

	for _, parent := range parents {
		prefix, _, chosen := strings.Cut(parent, "$[[")
		for candidate := range jobs {
			if candidate != name && (candidate == parent || (chosen && strings.HasPrefix(candidate, prefix))) {
				if retry := resolvedRetry(jobs, candidate); retry != nil {
					return retry
				}
			}
		}
	}

	return nil
}

// helperFetch is the fetch of the engine's helpers in a component's start:
// the loop that asks a forge it could not reach again.
func helperFetch(t *testing.T, path string) string {
	t.Helper()

	_, loop, found := strings.Cut(string(reporoot.ReadFile(t, path)), "for attempt in 1 2 3; do\n")
	if !found {
		t.Fatalf("%s: the helper fetch is not retried", path)
	}

	loop, _, found = strings.Cut(loop, "\n      done\n")
	if !found {
		loop, _, found = strings.Cut(loop, "\n    done\n")
	}

	if !found {
		t.Fatalf("%s: the helper fetch loop has no end", path)
	}

	return "for attempt in 1 2 3; do\n" + loop + "\ndone\n"
}

// The helpers are fetched before the engine exists, so the start itself
// tells a forge it could not reach from a fetch the forge refused: the first
// is asked three times and ends the job as unavailable, which GitLab retries
// once; the second ends it at once with a plain failure.
func TestHelperFetchRetriesOnlyAForgeItCouldNotReach(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"templates/release.yml", "templates/release-jobs.yml", "templates/check.yml", "templates/snapshot-image-jobs.yml"} {
		for said, want := range map[string]struct{ status, calls int }{
			"fatal: unable to access 'https://forge.example/o/r.git/': Could not resolve host: forge.example":                           {int(errs.ExitCodeUnavailable), 3},
			"fatal: unable to access 'https://forge.example/o/r.git/': Failed to connect to forge.example port 443 after 3 ms: Error":   {int(errs.ExitCodeUnavailable), 3},
			"fatal: unable to access 'https://forge.example/o/r.git/': The requested URL returned error: 404":                           {1, 1},
			"fatal: unable to access 'https://forge.example/o/r.git/': SSL certificate problem: unable to get local issuer certificate": {1, 1},
			"remote: Could not resolve host: forge.example\nfatal: remote error: upload-pack: not our ref abc":                          {1, 1},
		} {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")

			script := "git() { echo call >>\"$CALLS\"; printf '%s\\n' \"$SAID\" >&2; return 128; }\nsleep() { :; }\n" +
				"git_options=(); RC_HELPERS_DIR=h; work=w; SNAPSHOT_HELPERS=s; expected=abc; SNAPSHOT_ENGINE_SHA=abc\n" + helperFetch(t, path)

			command := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-euo", "pipefail", "-c", script)
			command.Env = []string{"PATH=/usr/bin:/bin", "CALLS=" + calls, "SAID=" + said}

			output, _ := command.CombinedOutput()

			made, _ := os.ReadFile(calls)
			if command.ProcessState.ExitCode() != want.status || strings.Count(string(made), "call") != want.calls {
				t.Errorf("%s: %q: exit %d after %d fetches, want %d after %d\n%s", path, said, command.ProcessState.ExitCode(), strings.Count(string(made), "call"), want.status, want.calls, output)
			}
		}

		// A fetch that succeeds is made once.
		command := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-euo", "pipefail", "-c",
			"git() { :; }\ngit_options=(); RC_HELPERS_DIR=h; work=w; SNAPSHOT_HELPERS=s; expected=abc; SNAPSHOT_ENGINE_SHA=abc\n"+helperFetch(t, path)+"echo fetched")
		command.Env = []string{"PATH=/usr/bin:/bin"}

		if output, err := command.CombinedOutput(); err != nil || string(output) != "fetched\n" {
			t.Errorf("%s: a fetch that succeeds: %v\n%s", path, err, output)
		}
	}
}

// Node reads its extra roots from its own variable. Every job start that
// gives the instance's CA to curl, Git and Go gives it to Node too, or npm
// refuses the instance's package registry.
func TestJobStartsGiveNodeTheInstanceCA(t *testing.T) {
	t.Parallel()

	templates, err := filepath.Glob(filepath.Join(reporoot.Path(t), "templates", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}

	starts := 0

	for _, file := range templates {
		body := string(reporoot.ReadFile(t, "templates/"+filepath.Base(file)))

		// Each place SSL_CERT_FILE is exported from the instance's CA.
		for _, export := range regexp.MustCompile(`(?m)^ *export .*\bSSL_CERT_FILE=.*\n(?: *#.*\n)*(.*)\n`).FindAllStringSubmatch(body, -1) {
			starts++

			if !regexp.MustCompile(`^ *export NODE_EXTRA_CA_CERTS=("\$\{CURL_CA_BUNDLE\}"|\$\{ca_bundle\})$`).MatchString(export[1]) {
				t.Errorf("%s: the CA goes to curl, Git and Go and not to Node:\n%s", file, export[0])
			}
		}
	}

	if starts != 4 {
		t.Fatalf("%d job starts export the instance's CA, want the 4 known", starts)
	}
}

// The export is made with the instance's CA and only with it.
func TestReleaseJobsGiveNodeTheInstanceCA(t *testing.T) {
	t.Parallel()

	_, trust, found := strings.Cut(string(reporoot.ReadFile(t, "templates/release-jobs.yml")), "    git_options=(-c core.hooksPath=/dev/null -c credential.helper=)\n")
	if !found {
		t.Fatal("missing trust setup")
	}

	trust, _, found = strings.Cut(trust, "    object_format=sha1\n")
	if !found {
		t.Fatal("missing end of trust setup")
	}

	dir := t.TempDir()

	public, private := filepath.Join(dir, "public.pem"), filepath.Join(dir, "private.pem")
	for file, body := range map[string]string{public: "public-root\n", private: "lab-root\n"} {
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	script := strings.NewReplacer("/etc/ssl/certs/ca-certificates.crt", public).Replace("git_options=()\n" + trust)

	for ca, want := range map[string]string{"": "\n", private: filepath.Join(dir, "ca-bundle.crt") + "\n"} {
		command := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-euo", "pipefail", "-c",
			script+"\nprintf '%s\\n' \"${NODE_EXTRA_CA_CERTS:-}\"")
		command.Env = []string{"PATH=/usr/bin:/bin", "RC_WORK=" + dir, "CI_SERVER_TLS_CA_FILE=" + ca}

		if output, err := command.CombinedOutput(); err != nil || string(output) != want {
			t.Errorf("instance CA %q: Node's extra roots are %q (%v), want %q", ca, output, err, want)
		}
	}
}
