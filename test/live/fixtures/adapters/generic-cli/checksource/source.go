// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/safeexec"
)

var (
	errSourceIdentity = errors.New("invalid original source identity or diagnostic policy")
	errSourceGitState = errors.New("original source already has Git state")
	errSourceTree     = errors.New("captured original source differs from the signed tree identity")
)

type sourceIdentity struct {
	Commit       string   `json:"commit"`
	Tree         string   `json:"tree"`
	ObjectFormat string   `json:"objectFormat"`
	LintPolicy   string   `json:"lintPolicy"`
	Excluded     []string `json:"excludedChecks"`
}

func readSourceIdentity() (sourceIdentity, error) {
	var identity sourceIdentity

	body, err := os.ReadFile(".gitlab/generic-cli-source.json")
	if err != nil {
		return identity, err
	}

	if err := json.Unmarshal(body, &identity); err != nil {
		return identity, err
	}

	length := map[string]int{"sha1": 40, "sha256": 64}[identity.ObjectFormat]
	if length == 0 || len(identity.Commit) != length || len(identity.Tree) != length ||
		strings.Trim(identity.Commit+identity.Tree, "0123456789abcdef") != "" ||
		identity.LintPolicy != "informational" || identity.Excluded == nil || len(identity.Excluded) != 0 {
		return identity, errSourceIdentity
	}

	return identity, nil
}

// genericCLIChecks runs the original Go repository gate, not the disposable CI
// overlay. Preparation carries the original Git tree as data; the signed request
// binds its tree identity. No original history, provider call or key is needed.
func genericCLIChecks(ctx context.Context) error {
	identity, err := readSourceIdentity()
	if err != nil {
		return err
	}

	source, err := filepath.Abs(".gitlab/generic-cli-original-source")
	if err != nil {
		return err
	}

	if _, err := os.Lstat(filepath.Join(source, ".git")); !os.IsNotExist(err) {
		return errSourceGitState
	}

	// Keep GPG test-agent sockets below Unix path limits even when the runner's
	// Buildah TMPDIR is deeply nested. This is private unit-test scratch space,
	// not a different HOME/XDG or a way around the live suite's held locks.
	temp, err := os.MkdirTemp("/tmp", "fci-check-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temp) }()

	var env []string

	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") && !strings.HasPrefix(value, "TMPDIR=") && !strings.HasPrefix(value, "GOMAXPROCS=") {
			env = append(env, value)
		}
	}

	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "TMPDIR="+temp, "GOMAXPROCS=2")
	command := func(name string, args ...string) *exec.Cmd {
		cmd := safeexec.Command(ctx, name, args...)
		cmd.Dir, cmd.Env = source, env
		cmd.Stderr = os.Stderr

		return cmd
	}
	run := func(name string, args ...string) error {
		cmd := command(name, args...)
		cmd.Stdout = os.Stdout

		return cmd.Run()
	}

	if err := run("git", "-c", "core.hooksPath=/dev/null", "init", "--quiet", "--object-format="+identity.ObjectFormat); err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(filepath.Join(source, ".git")) }()

	if err := run("git", "add", "--force", "--all"); err != nil {
		return err
	}

	tree, err := command("git", "write-tree").Output()
	if err != nil {
		return err
	}

	if strings.TrimSpace(string(tree)) != identity.Tree {
		return errSourceTree
	}

	if err := run("git", "-c", "core.hooksPath=/dev/null", "-c", "user.name=CLI fixture", "-c", "user.email=fixture@localhost",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "test: original source gate"); err != nil {
		return err
	}

	fmt.Printf("SOURCE_TREE_VERIFIED commit=%s tree=%s\n", identity.Commit, identity.Tree)

	return genericCLISteps(run)
}

func genericCLISteps(run func(string, ...string) error) error {
	for _, step := range [][]string{
		{"just", "_lint-release-platforms"},
		{"just", "--fmt", "--check"},
		{"go", "mod", "tidy", "-diff"},
		{"nanolinter", "check-tools"},
	} {
		if err := run(step[0], step[1:]...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(step, " "), err)
		}
	}

	fmt.Println("ACCEPTANCE POLICY: full original-source Nanolinter plan runs informationally; no exclusions; not a full just verify pass.")

	lintStatus := 0

	if err := run("nanolinter", "verify"); err != nil {
		var exit interface{ ExitCode() int }
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("nanolinter invocation: %w", err)
		}

		lintStatus = 1
	}

	fmt.Printf("NANOLINTER_DIAGNOSTIC exit=%d policy=informational excludedChecks=[]; full output/summary above retained in job log\n", lintStatus)

	return run("just", "test")
}
