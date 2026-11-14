// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package runnerlender

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runLend runs the lending script with exactly the given environment and a
// PATH that holds file utilities only: no curl, sudo or useradd, so
// validation must refuse before anything is installed or any user is created.
func runLend(t *testing.T, env map[string]string) (string, error) {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}

	tools := t.TempDir()

	for _, tool := range []string{"dirname", "basename", "id", "hostname", "grep", "cat", "mktemp", "env", "timeout"} {
		if path, lookErr := exec.LookPath(tool); lookErr == nil {
			if linkErr := os.Symlink(path, filepath.Join(tools, tool)); linkErr != nil {
				t.Fatal(linkErr)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bash, "lend-forgejo-runner.sh")
	cmd.Env = []string{"PATH=" + tools, "HOME=" + t.TempDir(), "LC_ALL=C"}

	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	var stderr bytes.Buffer

	cmd.Stderr = &stderr
	err = cmd.Run()

	return stderr.String(), err
}

func validLendEnvironment() map[string]string {
	return map[string]string{
		"FORGEJO_INSTANCE_URL":              "https://forge.example",
		"FORGEJO_RUNNER_REGISTRATION_TOKEN": "registration-canary",
		"RUNNER_LABELS":                     "gh-vm,arch=amd64",
		"LEND_TTL_MINUTES":                  "20",
	}
}

func TestLendScript_RefusesEveryBadInputBeforeTouchingTheMachine(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		key, value, want string
	}{
		"plain http instance":   {"FORGEJO_INSTANCE_URL", "http://forge.example", "FORGEJO_INSTANCE_URL must be an https:// URL"},
		"empty token":           {"FORGEJO_RUNNER_REGISTRATION_TOKEN", "", "FORGEJO_RUNNER_REGISTRATION_TOKEN is empty"},
		"label with a schema":   {"RUNNER_LABELS", "gh-vm:docker://node:20", "RUNNER_LABELS must be"},
		"label with shell text": {"RUNNER_LABELS", "gh-vm,$(id)", "RUNNER_LABELS must be"},
		"ttl too short":         {"LEND_TTL_MINUTES", "1", "LEND_TTL_MINUTES must be 2..180"},
		"ttl not a number":      {"LEND_TTL_MINUTES", "20m", "LEND_TTL_MINUTES must be 2..180"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env := validLendEnvironment()
			env[tc.key] = tc.value

			stderr, err := runLend(t, env)
			if err == nil || !strings.Contains(stderr, tc.want) {
				t.Fatalf("err=%v stderr=%q, want a refusal containing %q", err, stderr, tc.want)
			}

			if strings.Contains(stderr, "registration-canary") {
				t.Error("the registration token reached stderr")
			}
		})
	}
}

// With valid input the next thing the script needs is root, and it says so
// instead of trying: the tests never run it as root.
func TestLendScript_ValidInputThenRequiresRoot(t *testing.T) {
	t.Parallel()

	stderr, err := runLend(t, validLendEnvironment())
	if err == nil || !strings.Contains(stderr, "run as root") {
		t.Fatalf("err=%v stderr=%q, want the root requirement", err, stderr)
	}
}

func TestLendScript_IdleExpiryCannotClaimSuccessfulRetirement(t *testing.T) {
	t.Parallel()

	script, err := filepath.Abs("lend-forgejo-runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Sourcing defines functions only: no root check, install or registration.
	command := exec.CommandContext(t.Context(), "bash", "-c", `source "$1"; idle_expired fci-idle-witness 2`, "test", script)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}

	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "retirement is unconfirmed") || strings.Contains(string(output), "released.") {
		t.Fatalf("idle expiry: err=%v output=%q; want failure, not a retirement claim", err, output)
	}
}
