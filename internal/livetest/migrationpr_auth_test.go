// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Exercise the generated shell with a fake installer, never real credentials or
// a network request. The fake reports variable names only, even on failure.
func TestConsumerPRPublicInstallCredentialBoundary(t *testing.T) {
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	workflow, err := consumerPRWorkflow(publicShellPRFixture("linux", "engine", "digest"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(workflow), &doc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fake := `#!/bin/bash
set -euo pipefail
for name in FORGEJO_TOKEN MISE_FORGEJO_TOKEN GITHUB_TOKEN MISE_GITHUB_TOKEN; do
  if [[ -v $name ]]; then
    printf 'unexpected credential variable: %s\n' "$name" >&2
    exit 91
  fi
done
case "$*" in
  install) printf 'full install\n' >>"$CALLS" ;;
  'install aqua:casey/just@'*|'bin-paths aqua:casey/just@'*) printf '%s\n' "$*" >>"$CALLS" ;;
  *) exit 92 ;;
esac
if [[ ${FAIL_INSTALL:-0} == 1 && $1 == install ]]; then exit 42; fi
`
	if err := os.WriteFile(filepath.Join(dir, "mise"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1.58.0", "1.59.0"} {
		t.Run(version, func(t *testing.T) {
			calls := filepath.Join(t.TempDir(), "calls")
			pathOutput := filepath.Join(t.TempDir(), "path")
			// An explicit environment excludes ambient host credentials entirely.
			env := []string{"PATH=" + dir + ":/usr/bin:/bin", "CALLS=" + calls,
				"FORGEJO_PATH=" + pathOutput, "JUST_VERSION=" + version}
			for _, name := range []string{"FORGEJO_TOKEN", "MISE_FORGEJO_TOKEN", "GITHUB_TOKEN", "MISE_GITHUB_TOKEN"} {
				env = append(env, name+"=synthetic-local-forge-only")
			}
			var scripts []string
			for _, step := range doc.Jobs["units"].Steps {
				if step.Name == "Install all original public tools" || step.Name == "Install original CI Just selection" {
					scripts = append(scripts, step.Run)
				}
			}
			if len(scripts) != 2 {
				t.Fatal("expected full toolchain followed by original Just selection")
			}
			// A subsequent non-tool operation still has its repository credential.
			script := strings.Join(scripts, "\n") + "\n test -n \"${FORGEJO_TOKEN:?}\"\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("public install boundary: %v: %s", err, out)
			}
			got, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			want := "full install\ninstall aqua:casey/just@" + version + "\nbin-paths aqua:casey/just@" + version + "\n"
			if string(got) != want {
				t.Fatalf("tool installation requests changed: %q", got)
			}
			cmd = exec.Command("bash", "-c", scripts[0]+"\nexit 0\n")
			cmd.Env = append(env, "FAIL_INSTALL=1")
			if err := cmd.Run(); err == nil {
				t.Fatal("an unavailable tool must fail the public installation")
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 42 {
				t.Fatalf("expected installer failure to propagate, got %v", err)
			}
		})
	}
}
