// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// actionInput is one declared input of an action or a reusable workflow.
type actionInput struct {
	Required bool `yaml:"required"`
	Default  any  `yaml:"default"`
}

// step is one step of an action or a workflow job.
type step struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]any    `yaml:"with"`
}

// action is the part of actions/<name>/action.yml the behavior tests read.
type action struct {
	name   string
	Inputs map[string]actionInput `yaml:"inputs"`
	Runs   struct {
		Using string `yaml:"using"`
		Steps []step `yaml:"steps"`
	} `yaml:"runs"`
}

func loadYAML(t *testing.T, path string, into any) {
	t.Helper()

	if err := yaml.Unmarshal(reporoot.ReadFile(t, path), into); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func loadAction(t *testing.T, name string) action {
	t.Helper()

	document := action{name: name}
	loadYAML(t, ".forgejo/actions/"+name+"/action.yml", &document)

	return document
}

// requireSteps fails unless the action is composite with exactly n steps.
func (a action) requireSteps(t *testing.T, n int) {
	t.Helper()

	if a.Runs.Using != "composite" || len(a.Runs.Steps) != n {
		t.Fatalf("%s: using %q with %d steps, want composite with %d", a.name, a.Runs.Using, len(a.Runs.Steps), n)
	}
}

// step returns the step whose id or name is key.
func (a action) step(t *testing.T, key string) step {
	t.Helper()

	for _, candidate := range a.Runs.Steps {
		if candidate.ID == key || candidate.Name == key {
			return candidate
		}
	}

	t.Fatalf("%s has no step %q", a.name, key)

	return step{}
}

// script returns the step's run script with github.action_path rendered as the
// runner would; Bash would otherwise stop with "bad substitution".
func (a action) script(t *testing.T, key string) string {
	t.Helper()

	run := a.step(t, key).Run
	if strings.TrimSpace(run) == "" {
		t.Fatalf("%s step %q has no run script", a.name, key)
	}

	return strings.ReplaceAll(run, "${{ github.action_path }}", "${GITHUB_ACTION_PATH}")
}

// requireStepEnv fails unless step index binds exactly want: each variable the
// shell reads comes from the intended input, and no unreviewed variable exists.
func (a action) requireStepEnv(t *testing.T, index int, want map[string]string) {
	t.Helper()

	if got := a.Runs.Steps[index].Env; !maps.Equal(got, want) {
		t.Fatalf("%s step %d env = %q, want %q", a.name, index, got, want)
	}
}

// workflowStep returns the named step of a job in .forgejo/workflows/<file>.
func workflowStep(t *testing.T, file, job, name string) step {
	t.Helper()

	return jobStep(t, ".forgejo/workflows/"+file, job, name)
}

// jobStep returns the named step of a job in the workflow at path.
func jobStep(t *testing.T, path, job, name string) step {
	t.Helper()

	var document struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	loadYAML(t, path, &document)

	for _, candidate := range document.Jobs[job].Steps {
		if candidate.Name == name {
			return candidate
		}
	}

	t.Fatalf("%s job %s has no step %q", path, job, name)

	return step{}
}

// actionStep returns the run script of the step with id or name key.
func actionStep(t *testing.T, name, key string) string {
	t.Helper()

	return loadAction(t, name).script(t, key)
}

// sandbox is an isolated copy of the action layout: actions/<name>/ next to a
// bin/ holding a fake resolver and a fake forge-ci engine that records its
// argv. The action's own shell runs against it with an explicit environment.
type sandbox struct {
	t         *testing.T
	root      string
	actionDir string
	outputs   string
	argvLog   string
}

// fakeEngine records its argv, one argument per NUL-terminated record, appends
// FAKE_ENGINE_OUTPUT to the step outputs, and exits with FAKE_ENGINE_STATUS.
const fakeEngine = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\0' "$@" >"${FAKE_ARGV_LOG:?}"
if [[ -n "${FAKE_ENGINE_OUTPUT:-}" ]]; then printf '%s\n' "${FAKE_ENGINE_OUTPUT}" >>"${FORGEJO_OUTPUT:?}"; fi
exit "${FAKE_ENGINE_STATUS:-0}"
`

// fakeResolver stands in for bin/lib-forge-ci.sh and runs the adjacent fake.
const fakeResolver = `fetch_forge_ci() { :; }
verify_forge_ci() { test "${SIGNER_STRICT:-}" = 1; }
run_forge_ci() {
  "$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/forge-ci" "$@"
}
`

func newSandbox(t *testing.T, action string) *sandbox {
	t.Helper()
	root := t.TempDir()

	box := &sandbox{
		t: t, root: root, actionDir: filepath.Join(root, ".forgejo", "actions", action),
		outputs: filepath.Join(root, "outputs"), argvLog: filepath.Join(root, "argv"),
	}
	for _, directory := range []string{box.actionDir, filepath.Join(root, ".engine", "bin"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	box.write(".engine/bin/lib-forge-ci.sh", fakeResolver, 0o600)
	box.write(".engine/bin/forge-ci", fakeEngine, 0o700)

	return box
}

// write creates root-relative path with content and mode.
func (s *sandbox) write(path, content string, mode os.FileMode) string {
	s.t.Helper()

	full := filepath.Join(s.root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		s.t.Fatal(err)
	}

	if err := writeFile(full, content, mode); err != nil {
		s.t.Fatal(err)
	}

	return full
}

// writeFile writes content with mode. An executable is written with fork
// blocked: a child forked by a parallel test would otherwise inherit the open
// write descriptor until its exec, and running the file then fails with
// ETXTBSY.
func writeFile(path, content string, mode os.FileMode) error {
	if mode&0o111 != 0 {
		syscall.ForkLock.Lock()
		defer syscall.ForkLock.Unlock()
	}

	return os.WriteFile(path, []byte(content), mode)
}

type result struct {
	status         int
	stdout, stderr string
}

// run executes script with only the base environment and env. The outputs
// file and argv log start empty on every run.
func (s *sandbox) run(script string, env ...string) result {
	s.t.Helper()

	path := s.write("run.sh", script, 0o600)
	if err := os.WriteFile(s.outputs, nil, 0o600); err != nil {
		s.t.Fatal(err)
	}

	_ = os.Remove(s.argvLog)

	ctx, cancel := context.WithTimeout(s.t.Context(), time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", path)
	command.Dir = s.root
	command.Env = append([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(s.root, "home"), "TMPDIR=" + s.t.TempDir(),
		"LANG=C", "LC_ALL=C", "GITHUB_ACTION_PATH=" + s.actionDir, "FORGEJO_OUTPUT=" + s.outputs,
		"FAKE_ARGV_LOG=" + s.argvLog,
	}, env...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer

	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()

	status := 0

	exit := &exec.ExitError{}
	if errors.As(err, &exit) {
		status = exit.ExitCode()
	} else if err != nil {
		s.t.Fatalf("run action step: %v", err)
	}

	return result{status: status, stdout: stdout.String(), stderr: stderr.String()}
}

// argv returns the fake engine's arguments, or nil when it did not run.
func (s *sandbox) argv() []string {
	s.t.Helper()

	data, err := os.ReadFile(s.argvLog)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		s.t.Fatal(err)
	}

	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}

// output returns the step outputs file.
func (s *sandbox) output() string {
	s.t.Helper()

	data, err := os.ReadFile(s.outputs)
	if err != nil {
		s.t.Fatal(err)
	}

	return string(data)
}

// engineSandbox is a sandbox whose resolver is the layer's own and whose
// engine is the CLI built from this tree, with the fake tools on PATH and
// `forge-ci` there too, as setup-forge-ci leaves it in a job.
func engineSandbox(t *testing.T, action string, tools map[string]string) (*sandbox, []string) {
	t.Helper()

	engine := forgeCIEngine(t)
	box := newSandbox(t, action)
	box.write(".engine/bin/lib-forge-ci.sh", string(reporoot.ReadFile(t, ".engine/bin/lib-forge-ci.sh")), 0o600)
	for _, arch := range []string{"amd64", "arm64"} {
		file := ".engine/bin/forge-ci-linux-" + arch + ".sha256"
		box.write(file, string(reporoot.ReadFile(t, file)), 0o600)
	}
	box.write("tools/forge-ci", "#!/usr/bin/env bash\nexec '"+engine+"' \"$@\"\n", 0o700)

	for name, body := range tools {
		box.write("tools/"+name, body, 0o700)
	}

	return box, []string{
		"FORGE_CI_BIN=" + engine, "PATH=" + filepath.Join(box.root, "tools") + ":" + os.Getenv("PATH"),
		"FAKE_TOOL_LOG=" + filepath.Join(box.root, "tool-log"), "FORGEJO_ACTIONS=true",
	}
}

func readSandboxFile(t *testing.T, box *sandbox, path string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(box.root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}
