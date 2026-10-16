// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const buildMaven = "build-maven"

// fakeEngineCalls records every engine call, one line per call with its
// arguments separated by spaces, where the shared fake keeps only the last.
const fakeEngineCalls = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_ARGV_LOG:?}"
`

// mavenSandbox is a sandbox for build-maven with the action's own pin file
// beside it, as the runner checks the action out.
func mavenSandbox(t *testing.T, engine bool) (*sandbox, []string) {
	t.Helper()

	var (
		box *sandbox
		env []string
	)

	if engine {
		box, env = engineSandbox(t, buildMaven, map[string]string{"mvn": fakeMvn})
	} else {
		box = newSandbox(t, buildMaven)
		box.write(".forgejo/bin/forge-ci", fakeEngineCalls, 0o700)
	}

	box.write(".forgejo/actions/build-maven/versions.env", string(reporoot.ReadFile(t, ".forgejo/actions/build-maven/versions.env")), 0o600)

	return box, env
}

func mavenInputs(overrides ...string) []string {
	env := []string{
		"BUILD_MAVEN_VERSION=v1.2.3", "BUILD_MAVEN_COMMIT=abc123", "BUILD_MAVEN_BUILD_TYPE=build-type-v",
		"BUILD_MAVEN_WORKING_DIRECTORY=.", "BUILD_MAVEN_PROFILE=profile-v",
		"BUILD_MAVEN_SKIP_TESTS=skip-tests-v", "BUILD_MAVEN_BUILD_SBOM=build-sbom-v",
	}

	for _, override := range overrides {
		name, _, _ := strings.Cut(override, "=")
		env = slices.DeleteFunc(env, func(entry string) bool { return strings.HasPrefix(entry, name+"=") })
		env = append(env, override)
	}

	return env
}

// build-maven is GitHub's build-maven sequence in the Forgejo layer's shape:
// one `build maven run` with every input on its own flag, the Maven options
// and plugin pin the other two forges use, and the release version as a check,
// never as a rewrite: prepare-release.yml commits it. A build without a commit
// is refused before the engine starts, because in a release it may not fall
// back to the runner's; a build without a version checks none.
func TestBuildMavenForwardsEveryInput(t *testing.T) {
	t.Parallel()

	action := loadAction(t, buildMaven)
	action.requireSteps(t, 1)

	inputs := []string{"version", "commit", "build-type", "working-directory", "profile", "skip-tests", "build-sbom"}
	bound := map[string]string{}

	for _, input := range inputs {
		bound["BUILD_MAVEN_"+strings.ToUpper(strings.ReplaceAll(input, "-", "_"))] = "${{ inputs." + input + " }}"
	}

	action.requireStepEnv(t, 0, bound)

	for _, required := range []string{"commit", "build-type"} {
		if !action.Inputs[required].Required || action.Inputs[required].Default != nil {
			t.Errorf("input %s must be required and without a default", required)
		}
	}

	if action.Inputs["version"].Required {
		t.Error("input version must be optional: a pull request checks no version")
	}

	script := action.Runs.Steps[0].Run
	options := "--batch-mode --no-transfer-progress --errors --fail-at-end -Dstyle.color=always"

	box, _ := mavenSandbox(t, false)
	if got := box.run(script, mavenInputs()...); got.status != 0 {
		t.Fatalf("status %d stderr %s", got.status, got.stderr)
	}

	call := "build maven run --working-dir . --build-type build-type-v --profile profile-v --cli-opts " + options +
		" --skip-tests=skip-tests-v --build-sbom=build-sbom-v --sbom-tool-version 2.9.1 --expect-version "
	if got := readSandboxFile(t, box, "argv"); got != call+"1.2.3\n" {
		t.Fatalf("engine calls\n%s\nwant\n%s", got, call+"1.2.3")
	}

	unversioned, _ := mavenSandbox(t, false)
	if got := unversioned.run(script, mavenInputs("BUILD_MAVEN_VERSION=")...); got.status != 0 {
		t.Fatalf("no version: status %d stderr %s", got.status, got.stderr)
	}

	if got := readSandboxFile(t, unversioned, "argv"); got != call+"\n" {
		t.Fatalf("engine calls without a version\n%s\nwant\n%s", got, call)
	}

	for _, required := range []string{"COMMIT", "BUILD_TYPE", "WORKING_DIRECTORY", "SKIP_TESTS", "BUILD_SBOM"} {
		refused, _ := mavenSandbox(t, false)

		got := refused.run(script, mavenInputs("BUILD_MAVEN_"+required+"=")...)
		if got.status == 0 || refused.argv() != nil {
			t.Fatalf("an empty %s: status %d argv %q stderr %q", required, got.status, refused.argv(), got.stderr)
		}
	}
}

// Everything the job keeps is rooted at working-directory, so a path that
// leaves the workspace is refused before the engine runs.
func TestBuildMavenRefusesAWorkingDirectoryOutsideTheWorkspace(t *testing.T) {
	t.Parallel()

	script := actionStep(t, buildMaven, "Build Maven project")

	for _, directory := range []string{"/tmp/app", "..", "../app", "app/../..", "app/.."} {
		box, _ := mavenSandbox(t, false)

		got := box.run(script, mavenInputs("BUILD_MAVEN_WORKING_DIRECTORY="+directory)...)
		if got.status == 0 || box.argv() != nil || !strings.Contains(got.stderr, "working-directory must") {
			t.Errorf("working-directory %q: status %d argv %q stderr %q", directory, got.status, box.argv(), got.stderr)
		}
	}
}

// fakeMvn stands in for Maven: it logs each command line with the version the
// build sees, and writes the jar and the Build SBOM where Maven would.
const fakeMvn = `#!/usr/bin/env bash
set -euo pipefail
printf 'mvn %s [FORGE_CI_VERSION=%s FORGE_CI_COMMIT=%s]\n' "$*" "${FORGE_CI_VERSION:-}" "${FORGE_CI_COMMIT:-}" >>"${FAKE_TOOL_LOG:?}"
for argument in "$@"; do
  case "${argument}" in
    package) mkdir -p target; printf 'jar\n' >"target/app-$(sed -n 's|.*<version>\(.*\)</version>.*|\1|p' pom.xml).jar" ;;
    *cyclonedx*) mkdir -p target; printf '{"bomFormat":"CycloneDX"}\n' >target/bom.json ;;
  esac
done
`

// The Maven build step against the CLI of this tree, with a stand-in Maven, in
// a project below the workspace root: a POM that declares the release version
// builds, with the release's version and commit in the environment, the jar
// and the Build SBOM in the project's target/, and no versions:set anywhere.
// A POM that declares another version is refused before Maven packages.
func TestBuildMavenThroughTheEngine(t *testing.T) {
	t.Parallel()

	for _, declared := range []string{"1.2.3", "0.1.0-SNAPSHOT"} {
		t.Run(declared, func(t *testing.T) {
			t.Parallel()

			box, env := mavenSandbox(t, true)
			box.write("service/pom.xml", "<project><modelVersion>4.0.0</modelVersion><groupId>example.invalid</groupId>"+
				"<artifactId>app</artifactId><version>"+declared+"</version></project>\n", 0o600)

			got := box.run(actionStep(t, buildMaven, "Build Maven project"), append(env, mavenInputs(
				"FORGEJO_REF_NAME=release-request/v1.2.3", "FORGEJO_SHA="+strings.Repeat("f", 40),
				"BUILD_MAVEN_BUILD_TYPE=app", "BUILD_MAVEN_WORKING_DIRECTORY=service", "BUILD_MAVEN_PROFILE=",
				"BUILD_MAVEN_SKIP_TESTS=false", "BUILD_MAVEN_BUILD_SBOM=true")...)...)

			log := readSandboxFile(t, box, "tool-log")
			if strings.Contains(log, "versions:set") {
				t.Fatalf("the build rewrote the POM version:\n%s", log)
			}

			if declared != "1.2.3" {
				if got.status == 0 || strings.Contains(log, " package") || !strings.Contains(got.stderr, "release commit must carry the release version") {
					t.Fatalf("a POM at %s built as 1.2.3: status %d stderr %s\n%s", declared, got.status, got.stderr, log)
				}

				return
			}

			if got.status != 0 {
				t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
			}

			if !strings.Contains(log, "cyclonedx-maven-plugin:2.9.1") || !strings.Contains(log, "[FORGE_CI_VERSION=1.2.3 FORGE_CI_COMMIT=abc123]") {
				t.Fatalf("Maven was not asked for the pinned Build SBOM with the release's version and commit:\n%s", log)
			}

			for _, written := range []string{"service/target/app-1.2.3.jar", "service/target/bom.json"} {
				if readSandboxFile(t, box, written) == "" {
					t.Errorf("%s is empty", written)
				}
			}
		})
	}
}
