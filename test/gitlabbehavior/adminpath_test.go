// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The check and release jobs run the commands their caller lists, which call
// forge-ci by name. forge_ci_admin_path puts a forge-ci first on PATH that
// runs the verified engine with the administrator's four settings as flags,
// ahead of the command's own arguments, and admits only true or false.
func TestAdminPathRunsTheEngineWithTheAdministratorSettings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	engine := filepath.Join(dir, "engine")

	if err := os.WriteFile(engine, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	run := func(settings ...string) (string, error) {
		script := `set -euo pipefail
source "$1"
HELPERS_ENGINE_VERIFIED="$2"
forge_ci_admin_path "$3/admin-bin" "${@:4}"
forge-ci build maven run --working-dir 'a b'
`
		args := append([]string{"--noprofile", "--norc", "-c", script, "admin-path", filepath.Join(reporoot.Path(t), ".engine/bin/lib-forge-ci.sh"), engine, dir}, settings...)
		command := exec.CommandContext(t.Context(), componentBash(t), args...)
		command.Env = []string{"PATH=" + componentTools(t), "HOME=" + dir}

		output, err := command.CombinedOutput()

		return string(output), err
	}

	output, err := run("false", "true", "false", "true")
	want := "--enforce-lint=false\n--enforce-vulnerabilities=true\n--enforce-application-tests=false\n--enforce-reproducible-builds=true\nbuild\nmaven\nrun\n--working-dir\na b\n"

	if err != nil || output != want {
		t.Fatalf("the job's forge-ci ran %q (%v), want %q", output, err, want)
	}

	for _, settings := range [][]string{{"false", "true", "false"}, {"false", "true", "false", "yes"}, {"FALSE", "true", "true", "true"}, {"", "true", "true", "true"}} {
		if output, err := run(settings...); err == nil || strings.Contains(output, "--enforce-") {
			t.Errorf("settings %q were admitted: %s", settings, output)
		}
	}
}

// After the job's start, every spelling a command can use for the engine,
// forge-ci on PATH and "$FORGE_CI_BIN", is the wrapper that carries the
// settings, even with the layer's own bin directory on PATH. The start of
// each job is run as the template writes it, from the wrapper's set-up to
// the end of the part that precedes the commands.
func TestJobStartLeavesOnlyTheWrapperToRunTheEngine(t *testing.T) {
	t.Parallel()

	for _, start := range []struct{ file, from, to, role, wrapper string }{
		{"templates/check.yml", "forge_ci_admin_path ", "  script:", "", "helpers/admin-bin/forge-ci"},
		{"templates/release-jobs.yml", `case "${rc_job_role}" in`, `source "${RC_HELPERS_DIR}/.gitlab/bin/release.sh"`, "checks", "work/admin-bin/forge-ci"},
		{"templates/release-jobs.yml", `case "${rc_job_role}" in`, `source "${RC_HELPERS_DIR}/.gitlab/bin/release.sh"`, "build", "work/admin-bin/forge-ci"},
	} {
		segment := startSegment(t, start.file, start.from, start.to)
		dir := t.TempDir()
		helpers := filepath.Join(dir, "helpers")

		lib, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".engine/bin/lib-forge-ci.sh"))
		if err != nil {
			t.Fatal(err)
		}

		for path, body := range map[string]string{
			"helpers/.engine/bin/lib-forge-ci.sh": string(lib),
			// The layer's own forge-ci, which must never win.
			"helpers/.engine/bin/forge-ci": "#!/bin/sh\necho layer-forge-ci\n",
			"engine":                       "#!/bin/sh\nprintf '%s ' \"$@\"; echo\n",
		} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
		}

		script := `set -euo pipefail
source "$HELPERS/.engine/bin/lib-forge-ci.sh"
HELPERS_ENGINE_VERIFIED="$ENGINE"
export FORGE_CI_BIN="${HELPERS_ENGINE_VERIFIED}"
export PATH="${HELPERS_ROOT}/bin:${PATH}"
` + segment + `
echo "resolved=$(command -v forge-ci)"
echo "bin=${FORGE_CI_BIN}"
forge-ci by-name
"${FORGE_CI_BIN}" by-variable
`
		command := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-c", script)
		command.Dir = dir
		command.Env = []string{
			"PATH=" + componentTools(t), "HOME=" + dir, "HELPERS=" + helpers, "ENGINE=" + filepath.Join(dir, "engine"),
			"RC_HELPERS_DIR=" + helpers, "RC_WORK=" + filepath.Join(dir, "work"), "rc_job_role=" + start.role,
			"RC_ADMIN_LINT=false", "RC_ADMIN_VULNERABILITIES=true", "RC_ADMIN_APPLICATION_TESTS=false", "RC_ADMIN_REPRODUCIBLE_BUILDS=true",
			"CI_PROJECT_DIR=" + dir, "CI_SERVER_URL=https://gitlab.invalid", "CI_REGISTRY=registry.gitlab.invalid", "RC_SERVER_URL=https://gitlab.invalid", "CI_PROJECT_PATH=team/app",
			"CI_COMMIT_SHA=" + strings.Repeat("a", 40), "CI_DEFAULT_BRANCH=main", "consumer_object_format=sha1",
		}

		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: the start failed: %v\n%s", start.file, start.role, err, output)
		}

		flags := "--enforce-lint=false --enforce-vulnerabilities=true --enforce-application-tests=false --enforce-reproducible-builds=true "

		wrapper := filepath.Join(dir, start.wrapper)
		for _, want := range []string{"resolved=" + wrapper + "\n", "bin=" + wrapper + "\n", flags + "by-name", flags + "by-variable"} {
			if !strings.Contains(string(output), want) {
				t.Errorf("%s %s: after the start, %q is missing; the engine is reachable without the settings:\n%s", start.file, start.role, want, output)
			}
		}

		if strings.Contains(string(output), "layer-forge-ci") {
			t.Errorf("%s %s: forge-ci resolved to the layer's own, not the wrapper:\n%s", start.file, start.role, output)
		}
	}
}

// startSegment is the shell of a template from the line containing from up
// to the line starting with to, dedented to the first line's indentation.
func startSegment(t *testing.T, file, from, to string) string {
	t.Helper()

	lines := strings.Split(string(reporoot.ReadFile(t, file)), "\n")
	begin := -1

	for index, line := range lines {
		if begin < 0 && strings.Contains(line, from) && strings.Contains(strings.Join(lines[index:min(index+4, len(lines))], "\n"), "forge_ci_admin_path") {
			begin = index

			continue
		}

		ends := strings.HasPrefix(strings.TrimLeft(line, " "), to)
		if strings.HasPrefix(to, " ") {
			ends = strings.HasPrefix(line, to)
		}

		if begin >= 0 && ends {
			indent := len(lines[begin]) - len(strings.TrimLeft(lines[begin], " "))

			kept := make([]string, 0, index-begin)

			for _, segment := range lines[begin:index] {
				if len(segment) >= indent {
					segment = segment[indent:]
				}

				kept = append(kept, segment)
			}

			return strings.Join(kept, "\n")
		}
	}

	t.Fatalf("%s: no start from %q to %q", file, from, to)

	return ""
}
