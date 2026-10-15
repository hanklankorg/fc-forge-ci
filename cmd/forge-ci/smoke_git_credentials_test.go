//go:build smoke

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

type gitCredentialCommand struct {
	path     []string
	required []string
}

func gitCredentialCommands(t *testing.T) []gitCredentialCommand {
	t.Helper()
	root := t.TempDir()

	return []gitCredentialCommand{
		{[]string{"platform", "checkout"}, []string{"--repository", "owner/repo", "--server-url", "https://forge.invalid", "--ref", "main", "--object-format", "sha1", "--workspace", filepath.Join(root, "checkout")}},
		{[]string{"version", "render-changelog"}, []string{"--tag", "v1.2.3", "--repository-url", "https://forge.invalid/owner/repo", "--changelog-config", filepath.Join(root, "changelog.yml"), "--commit-body-config", filepath.Join(root, "commit.yml")}},
		{[]string{"release", "validate-request"}, []string{"--release-request", "release-request/v1.2.3", "--tag", "v1.2.3"}},
		{[]string{"release", "validate-tag"}, []string{"--tag", "v1.2.3", "--release-sha", strings.Repeat("a", 40)}},
		{[]string{"version", "commit-push"}, []string{"--branch", "main", "--author-name", "Test", "--author-email", "test@example.invalid", "--message", "chore: fixture", "--file-pattern", "CHANGELOG.md"}},
		{[]string{"version", "tag-release"}, []string{"--tag", "v1.2.3"}},
		{[]string{"version", "commit-changelog-release"}, []string{"--tag", "v1.2.3", "--repository", "owner/repo", "--git-url", "ssh://git@forge.invalid/owner/repo.git", "--author-name", "Test", "--author-email", "test@example.invalid"}},
	}
}

func TestBinary_GitCredentialsRejectLiteralTokenArguments(t *testing.T) {
	testenv.New(t)
	bin := buildBinary(t)

	const canary = "literal-token-argument-canary"

	for _, command := range gitCredentialCommands(t) {
		t.Run(strings.Join(command.path, "/"), func(t *testing.T) {
			for _, flags := range [][]string{{"--token", canary}, {"--token=" + canary}} {
				args := append(append([]string{}, command.path...), flags...)

				stdout, stderr, code := runBinary(t, bin, append(args, "--help")...)
				if code != 2 {
					t.Errorf("literal token argument exited %d, want usage refusal", code)
				}

				if strings.Contains(stdout+stderr, canary) {
					t.Error("literal token was echoed by the refusal")
				}
			}
		})
	}
}

func TestBinary_GitCredentialsFileFailuresBeforeEffects(t *testing.T) {
	testenv.New(t)
	bin := buildBinary(t)
	// No Git or signing tool can be launched even if a command regresses.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_SERVER_URL", "https://github.invalid")
	t.Setenv("GITHUB_TOKEN", "ambient-token-canary")
	t.Setenv("RELEASE_TOKEN", "release-token-canary")
	missing := filepath.Join(t.TempDir(), "missing-token")

	for _, command := range gitCredentialCommands(t) {
		t.Run(strings.Join(command.path, "/"), func(t *testing.T) {
			args := append(append([]string{}, command.path...), command.required...)
			args = append(args, "--token-file", missing)

			stdout, stderr, code := runBinary(t, bin, args...)
			if code != 66 || !strings.Contains(stderr, "read secret") || !strings.Contains(stderr, missing) {
				t.Errorf("missing token file: exit=%d stderr=%q; want a classified secret-read refusal", code, stderr)
			}

			if stdout != "" || strings.Contains(stderr, "ambient-token-canary") || strings.Contains(stderr, "release-token-canary") {
				t.Error("secret-read refusal emitted data or credential material")
			}
		})
	}
}

func TestBinary_GitCredentialsExposeFileInput(t *testing.T) {
	testenv.New(t)
	bin := buildBinary(t)
	file := filepath.Join(t.TempDir(), "token")

	const canary = "file-token-help-canary"
	if err := os.WriteFile(file, []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, command := range gitCredentialCommands(t) {
		args := append(append([]string{}, command.path...), "--token-file", file, "--help")

		stdout, stderr, code := runBinary(t, bin, args...)
		if code != 0 || !strings.Contains(stdout, "--token-file string") || strings.Contains(stdout, "--token string") {
			t.Errorf("%v: wrong token interface, exit=%d stderr=%q", command.path, code, stderr)
		}

		if strings.Contains(stdout+stderr, canary) {
			t.Errorf("%v: help disclosed file contents", command.path)
		}
	}
}

func TestBinary_ChangelogRepositoryBeforeToken(t *testing.T) {
	testenv.New(t)
	bin := buildBinary(t)
	missing := filepath.Join(t.TempDir(), "missing-token")

	stdout, stderr, code := runBinary(t, bin, "--provider", "github", "version", "commit-changelog-release",
		"--tag", "v1.2.3", "--repository", "Group/Sub/Repo", "--git-url", "ssh://git@forge.invalid/Group/Sub/Repo.git", "--token-file", missing)
	if code != 2 || !strings.Contains(stderr, "without subgroups") || strings.Contains(stderr, missing) || stdout != "" {
		t.Fatalf("repository preflight: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestBinary_ChangelogReleaseRejectsSharedSecretStdin(t *testing.T) {
	testenv.New(t)
	bin := buildBinary(t)
	commands := gitCredentialCommands(t)
	command := commands[len(commands)-1]
	args := append(append([]string{}, command.path...), command.required...)
	args = append(args, "--private-key-file", "-", "--token-file", "-")

	stdout, stderr, code := runBinary(t, bin, args...)
	if code != 2 || !strings.Contains(stderr, "--private-key-file and --token-file") || stdout != "" {
		t.Fatalf("shared stdin: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
