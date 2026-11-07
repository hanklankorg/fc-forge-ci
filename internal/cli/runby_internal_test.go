// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func helpOf(t *testing.T, args ...string) string {
	t.Helper()

	var out bytes.Buffer

	root := New(BuildInfo{Version: "dev"})
	root.Writer = &out

	if err := root.Run(context.Background(), append([]string{"forge-ci"}, args...)); err != nil {
		t.Fatalf("%v: %v", args, err)
	}

	return out.String()
}

func TestHelpNamesTheWorkflowsOfAStepAndShortensGlobals(t *testing.T) {
	// Not parallel: urfave/cli help writes through package-level printers.
	step := helpOf(t, "release", "publish", "--help")
	if !strings.Contains(step, "RUN BY:\n   A workflow step: ") || !strings.Contains(step, "release-create-github.yml") {
		t.Errorf("release publish help has no RUN BY naming its workflow:\n%s", step)
	}

	if strings.Contains(step, "--enforce-lint string") || !strings.Contains(step, "--enforce-application-tests,") {
		t.Errorf("subcommand help must fold the global flags into one line:\n%s", step)
	}

	setup := helpOf(t, "setup", "signer", "--help")
	if strings.Contains(setup, "RUN BY") {
		t.Errorf("a command run at a terminal must not get RUN BY:\n%s", setup)
	}

	// A command on the run-directly list says so, not "a workflow step", and
	// RUN BY points at files a person can open.
	login := helpOf(t, "container", "login", "--help")
	if !strings.Contains(login, "Run it yourself; the reusable workflows also run it: ") ||
		!strings.Contains(login, "https://github.com/diggsweden/forge-ci/tree/") {
		t.Errorf("container login help contradicts the run-directly list or has no source link:\n%s", login)
	}

	root := helpOf(t, "--help")
	if !strings.Contains(root, "--enforce-lint string") {
		t.Errorf("root help must keep the global flags in full")
	}

	for _, want := range []string{"Exit codes:", "   78  ", "asset-retention", "completion bash"} {
		if !strings.Contains(root, want) {
			t.Errorf("root help lacks %q", want)
		}
	}
}

// The usage hint names no single workflow: picking one put Forgejo's file in
// front of a GitHub user.
func TestRunByHintNamesNoSingleForge(t *testing.T) {
	// Not parallel: urfave/cli help writes through package-level printers.
	var out bytes.Buffer

	root := New(BuildInfo{Version: "dev"})
	root.Writer = &out

	// Running sets each command's parent, which the hint's path is read from.
	if err := root.Run(context.Background(), []string{"forge-ci", "version", "bump", "--help"}); err != nil {
		t.Fatal(err)
	}

	bump := root.Command("version").Command("bump")
	if hint := runByHint(bump); !strings.Contains(hint, "RUN BY in --help") || strings.Contains(hint, ".forgejo") || strings.Contains(hint, ".github") {
		t.Errorf("hint = %q", hint)
	}
}
