// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

// The version workflows carry no build tag, like the probe builders: the live
// scenario sends them to a forge, and the fixtures below run the same scripts
// offline against a stand-in product, so an ordinary test run checks the
// command line a lab run would execute.

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	// versionReleaseTag is the final tag the version workflows ask for.
	versionReleaseTag = "v1.4.2"

	// The release identity. No job reads it from the environment: it reaches
	// the product by flag, so the flags are what the scenario exercises.
	versionAuthorName  = "Version Probe"
	versionAuthorEmail = "version-probe@invalid.example"

	// versionChangelog and versionCommitMessage are the two files a renderer
	// hands the command; the job writes them before it runs the product.
	versionChangelog     = "# Changelog\n\n## v1.4.2\n\n- the version probe release\n"
	versionCommitMessage = "chore(release): bump to v1.4.2\n\nRelease notes are in CHANGELOG.md.\n"

	// versionSigningKeySecret is the repository secret the jobs read the
	// throwaway deploy key from.
	versionSigningKeySecret = "RELEASE_SSH_SIGNING_KEY"

	// The two workflows, each started by its own tag: the release pushes
	// main and the final tag, and neither push may start a second run.
	versionDryRunWorkflow  = "version-dry-run"
	versionReleaseWorkflow = "version-release"
)

// versionRelease is what the version workflows ask the product to release,
// and the SSH URL they push to with the host key pin of its host.
type versionRelease struct {
	Repository  string
	GitURL      string
	Fingerprint string
}

// versionTrigger is the tag whose push starts one of the version workflows.
func versionTrigger(workflow string) string { return workflow + "/" + versionReleaseTag }

// versionWorkflow is one Forgejo workflow that runs command, the changelog
// release verb, in a checkout of the scratch repository: as a dry run, or for
// real followed by a version bump of a scratch manifest.
//
// The job checks out and names the branch as prepare-release.yml does, stages
// the rendered changelog and commit message beside a file no release owns,
// and passes every release input by flag.
func versionWorkflow(prelude string, release versionRelease, command []string, dryRun bool) string {
	name, step, body := versionReleaseWorkflow, "sign and push the changelog release", versionReleaseScript
	if dryRun {
		name, step, body = versionDryRunWorkflow, "preview the changelog release", versionDryRunScript
	}

	script := prelude + "\n" + strings.NewReplacer(
		"__COMMAND__", strings.Join(command, " "),
		"__CHANGELOG__", shellLines(versionChangelog),
		"__COMMIT_MESSAGE__", shellLines(versionCommitMessage),
	).Replace(versionStageScript+body)

	return `on:
  push:
    tags:
      - '` + name + `/*'
jobs:
  version:
    runs-on: ubuntu-latest
    steps:
      - name: ` + step + `
        env:
          CHECKOUT_REF: ${{ forgejo.sha }}
          SSH_SIGNING_KEY: ${{ secrets.` + versionSigningKeySecret + ` }}
          PROBE_TAG: ` + strconv.Quote(versionReleaseTag) + `
          PROBE_REPOSITORY: ` + strconv.Quote(release.Repository) + `
          PROBE_GIT_URL: ` + strconv.Quote(release.GitURL) + `
          PROBE_HOST_KEY_FINGERPRINT: ` + strconv.Quote(release.Fingerprint) + `
          PROBE_AUTHOR_NAME: ` + strconv.Quote(versionAuthorName) + `
          PROBE_AUTHOR_EMAIL: ` + strconv.Quote(versionAuthorEmail) + `
        run: |
          ` + indent(script, 10) + `
`
}

// shellLines spells a file as the arguments of printf '%s\n', one per line.
func shellLines(content string) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	for index, line := range lines {
		lines[index] = "'" + line + "'"
	}

	return strings.Join(lines, " ")
}

// versionStageScript leaves the checkout as the signing job of
// prepare-release.yml finds it: on the release branch, with the rendered
// files in place.
const versionStageScript = `
# The release pushes with ssh and signs with ssh-keygen. A job image without
# them is the fixture's gap and must not read as the product's answer.
for tool in git ssh ssh-keygen ssh-keyscan; do
  if ! command -v "$tool" >/dev/null; then
    echo "FAIL: the job image has no $tool; this is a fixture gap, not a result"
    exit 1
  fi
done

run_product platform checkout --fetch-all-refs
git -c core.hooksPath=/dev/null checkout -b main
before=$(git rev-parse HEAD)

printf '%s\n' __CHANGELOG__ > CHANGELOG.md
printf '%s\n' __COMMIT_MESSAGE__ > commit-msg.txt
printf '%s\n' 'not part of the release' > unrelated.txt
`

// versionDryRunScript previews the release as the release job would make it:
// with the signing key and the pinned host key, over which the preview asks
// the release URL whether the tag exists. It signs and pushes nothing.
const versionDryRunScript = `
run_product __COMMAND__ --dry-run \
  --tag "$PROBE_TAG" --branch main --repository "$PROBE_REPOSITORY" \
  --git-url "$PROBE_GIT_URL" --host-key-fingerprint "$PROBE_HOST_KEY_FINGERPRINT" \
  --author-name "$PROBE_AUTHOR_NAME" --author-email "$PROBE_AUTHOR_EMAIL"

# The preview must have reached the mutations it skips, and skipped them here
# too: no commit, no tag, and the message file left for the real run.
grep -qF '[dry-run] would push main to origin (no force)' product-out.txt
grep -qF "[dry-run] would push tag $PROBE_TAG to origin (no force)" product-out.txt
test "$(git rev-parse HEAD)" = "$before"
test -z "$(git tag --list "$PROBE_TAG")"
test -f commit-msg.txt
`

// versionReleaseScript makes the release, then bumps a scratch manifest.
//
// The bump comes second so that a failure of it leaves the release, which the
// host reads, already made. Gradle is the ecosystem because its bump rewrites
// one line of a properties file and needs no build tool in the job image.
const versionReleaseScript = `
run_product __COMMAND__ \
  --tag "$PROBE_TAG" --branch main --repository "$PROBE_REPOSITORY" \
  --git-url "$PROBE_GIT_URL" --host-key-fingerprint "$PROBE_HOST_KEY_FINGERPRINT" \
  --author-name "$PROBE_AUTHOR_NAME" --author-email "$PROBE_AUTHOR_EMAIL"

# The command removes the message it committed and leaves what it never owned.
test ! -e commit-msg.txt
test -f unrelated.txt

mkdir bump-scratch
printf '%s\n' 'org.gradle.caching=true' 'version=0.0.1' > bump-scratch/gradle.properties
run_product version bump --project-type gradle --version "${PROBE_TAG#v}" --working-dir bump-scratch
test "$(cat bump-scratch/gradle.properties)" = "$(printf '%s\n' 'org.gradle.caching=true' "version=${PROBE_TAG#v}")"
`

// versionFixtureFingerprint is the pin of the stand-in host key below.
const versionFixtureFingerprint = "SHA256:fixtureHostKeyPin+/0123456789abcdefghijklmnop"

// versionStandIn replaces the prelude offline. Its run_product records each
// invocation, one line of <argument> cells, and does to the working directory
// only what the script goes on to check; git answers as a fresh checkout
// would, and the OpenSSH tools are only looked for.
const versionStandIn = `run_product() {
  printf '<%s>' "$@" >> "$FIXTURE_DIR/calls"
  printf '\n' >> "$FIXTURE_DIR/calls"
  : > product-out.txt
  case "$1 $2" in
    'version commit-changelog-release')
      cp CHANGELOG.md commit-msg.txt unrelated.txt "$FIXTURE_DIR/"
      if [ "$3" = --dry-run ]; then
        printf '%s\n' "$FIXTURE_NARRATION" > product-out.txt
      else
        rm commit-msg.txt
      fi
      ;;
    'version bump')
      printf '%s\n' 'org.gradle.caching=true' "version=$FIXTURE_BUMPED" > bump-scratch/gradle.properties
      ;;
  esac
}
git() {
  case "$*" in
    'rev-parse HEAD') echo 0123456789abcdef0123456789abcdef01234567 ;;
  esac
}
ssh() { :; }
ssh-keyscan() { :; }
ssh-keygen() { :; }`

// versionFixture decodes one version workflow and returns its only step.
func versionFixture(t *testing.T, dryRun bool) (probeStep, []string) {
	t.Helper()

	release := versionRelease{Repository: "owner/fci-version", GitURL: "ssh://git@forge.invalid:2222/owner/fci-version.git", Fingerprint: versionFixtureFingerprint}

	var workflow struct {
		On struct {
			Push struct {
				Tags []string `yaml:"tags"`
			} `yaml:"push"`
		} `yaml:"on"`
		Jobs map[string]struct {
			RunsOn string      `yaml:"runs-on"`
			Steps  []probeStep `yaml:"steps"`
		} `yaml:"jobs"`
	}

	body := versionWorkflow(versionStandIn, release, []string{"version", "commit-changelog-release"}, dryRun)
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}

	job, ok := workflow.Jobs["version"]
	if !ok || len(workflow.Jobs) != 1 || len(job.Steps) != 1 || job.RunsOn != "ubuntu-latest" {
		t.Fatalf("the workflow is not one single-step job on the probe runner: %+v", workflow.Jobs)
	}

	return job.Steps[0], workflow.On.Push.Tags
}

// versionFixtureEnv is the step's environment as a runner would hand it over,
// with the two expressions resolved and the stand-in's answers added.
func versionFixtureEnv(step probeStep, dir string, overrides map[string]string) []string {
	values := map[string]string{
		"FIXTURE_DIR":    dir,
		"FIXTURE_BUMPED": strings.TrimPrefix(versionReleaseTag, "v"),
		"FIXTURE_NARRATION": "[dry-run] would push main to origin (no force)\n" +
			"[dry-run] would push tag " + versionReleaseTag + " to origin (no force)",
	}

	maps.Copy(values, step.Env)

	values["CHECKOUT_REF"] = "0123456789abcdef0123456789abcdef01234567"
	values["SSH_SIGNING_KEY"] = "fixture key"

	maps.Copy(values, overrides)

	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}

	return env
}

func TestVersionWorkflows_BindTheReleaseInputs(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"CHECKOUT_REF":               "${{ forgejo.sha }}",
		"SSH_SIGNING_KEY":            "${{ secrets.RELEASE_SSH_SIGNING_KEY }}",
		"PROBE_TAG":                  "v1.4.2",
		"PROBE_REPOSITORY":           "owner/fci-version",
		"PROBE_GIT_URL":              "ssh://git@forge.invalid:2222/owner/fci-version.git",
		"PROBE_HOST_KEY_FINGERPRINT": versionFixtureFingerprint,
		"PROBE_AUTHOR_NAME":          "Version Probe",
		"PROBE_AUTHOR_EMAIL":         "version-probe@invalid.example",
	}

	for dryRun, trigger := range map[bool]string{true: "version-dry-run/*", false: "version-release/*"} {
		step, tags := versionFixture(t, dryRun)

		// Its own tag alone starts a workflow: the release pushes main and
		// the final tag, and a second run of either would release twice.
		if !reflect.DeepEqual(tags, []string{trigger}) {
			t.Errorf("dry-run=%t starts on %v, want only %s", dryRun, tags, trigger)
		}

		if !reflect.DeepEqual(step.Env, want) {
			t.Errorf("dry-run=%t binds %v, want %v", dryRun, step.Env, want)
		}

		// No release input may come from a variable the product reads by
		// itself: the flags are what the scenario exercises.
		for _, source := range []string{"RELEASE_TAG", "TAG_NAME", "RELEASE_GIT_URL", "RELEASE_GIT_HOST_KEY_FINGERPRINT", "GIT_USER_NAME", "GIT_USER_EMAIL", "COMMIT_AUTHOR_NAME", "COMMIT_AUTHOR_EMAIL"} {
			if strings.Contains(step.Run, source) || step.Env[source] != "" {
				t.Errorf("dry-run=%t hands the product %s through the environment", dryRun, source)
			}
		}
	}

	if versionTrigger(versionDryRunWorkflow) != "version-dry-run/v1.4.2" || versionTrigger(versionReleaseWorkflow) != "version-release/v1.4.2" {
		t.Error("the trigger tags do not match the workflows' tag filters")
	}
}

func TestVersionWorkflows_RunTheExactCommandLine(t *testing.T) {
	t.Parallel()

	const (
		checkout = "<platform><checkout><--fetch-all-refs>"
		flags    = "<--tag><v1.4.2><--branch><main><--repository><owner/fci-version>" +
			"<--git-url><ssh://git@forge.invalid:2222/owner/fci-version.git>" +
			"<--host-key-fingerprint><" + versionFixtureFingerprint + ">" +
			"<--author-name><Version Probe><--author-email><version-probe@invalid.example>"
	)

	for _, tc := range []struct {
		name   string
		dryRun bool
		calls  []string
	}{
		{"dry-run", true, []string{checkout, "<version><commit-changelog-release><--dry-run>" + flags}},
		{"release", false, []string{
			checkout, "<version><commit-changelog-release>" + flags,
			"<version><bump><--project-type><gradle><--version><1.4.2><--working-dir><bump-scratch>",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			step, _ := versionFixture(t, tc.dryRun)
			dir := t.TempDir()

			if err := runProbeShell(t, step.Run, versionFixtureEnv(step, dir, nil)...); err != nil {
				t.Fatalf("the job refused a product that did what it asked: %v", err)
			}

			if calls := versionFixtureCalls(t, dir); !reflect.DeepEqual(calls, tc.calls) {
				t.Errorf("the job ran\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(tc.calls, "\n"))
			}

			// What the job staged for the product is what the host expects
			// the release commit to hold, and to leave out.
			for file, content := range map[string]string{
				"CHANGELOG.md":   versionChangelog,
				"commit-msg.txt": versionCommitMessage,
				"unrelated.txt":  "not part of the release\n",
			} {
				staged, err := os.ReadFile(filepath.Join(dir, file))
				if err != nil || string(staged) != content {
					t.Errorf("the job staged %s as %q (%v), want %q", file, staged, err, content)
				}
			}
		})
	}
}

func TestVersionWorkflows_RefuseAProductThatDidNotDoIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		dryRun   bool
		override map[string]string
	}{
		{"dry-run/no-tag-push-narrated", true, map[string]string{"FIXTURE_NARRATION": "[dry-run] would push main to origin (no force)"}},
		{"dry-run/no-branch-push-narrated", true, map[string]string{"FIXTURE_NARRATION": "[dry-run] would push tag v1.4.2 to origin (no force)"}},
		{"release/manifest-not-bumped", false, map[string]string{"FIXTURE_BUMPED": "0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			step, _ := versionFixture(t, tc.dryRun)
			dir := t.TempDir()

			if err := runProbeShell(t, step.Run, versionFixtureEnv(step, dir, tc.override)...); err == nil {
				t.Fatal("the job passed")
			}

			// The job failed on what the product left, not before asking it.
			if !slices.ContainsFunc(versionFixtureCalls(t, dir), func(call string) bool {
				return strings.HasPrefix(call, "<version><commit-changelog-release>")
			}) {
				t.Error("the product was never asked to release")
			}
		})
	}
}

// versionFixtureCalls is what the stand-in product was asked, in order.
func versionFixtureCalls(t *testing.T, dir string) []string {
	t.Helper()

	recorded, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		t.Fatal(err)
	}

	return strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
}
