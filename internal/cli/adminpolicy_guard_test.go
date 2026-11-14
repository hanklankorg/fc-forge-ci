// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	urfavecli "github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/testutil/cliflags"
)

// The enforcement settings have one source: the command line that runs the
// engine. Each is declared once, on the root, so
// every command honours it, with no value source at all, so no environment
// variable or plan file supplies one, and may be given once, so a second
// spelling cannot replace the first. No other flag reads an environment
// variable named like a setting either: the engine refuses those names.
func TestAdminSettingsHaveNoSourceButTheCommandLine(t *testing.T) {
	t.Parallel()

	root := cli.New(cli.BuildInfo{Version: "dev"})
	settings := map[string]bool{}

	for _, check := range enforcement.Checks {
		settings[enforcement.Setting(check)] = false
	}

	var walk func(path string, cmd *urfavecli.Command)

	walk = func(path string, cmd *urfavecli.Command) {
		for _, flag := range cmd.Flags {
			name := flag.Names()[0]
			chain := cliflags.Sources(t, flag)

			for _, key := range chain.EnvKeys() {
				if err := enforcement.RefuseEnvironment([]string{key + "="}); err != nil {
					t.Errorf("%s --%s reads $%s, a name the engine refuses", path, name, key)
				}
			}

			if _, admin := settings[name]; !admin {
				continue
			}

			if cmd != root {
				t.Errorf("%s declares --%s; only the root declares administrator settings", path, name)
			}

			if len(chain.Chain) != 0 {
				t.Errorf("--%s has a value source besides the command line: %s", name, chain.GoString())
			}

			if setting, ok := flag.(*urfavecli.StringFlag); !ok || !setting.OnlyOnce || setting.Validator == nil {
				t.Errorf("--%s is not a once-only, validated string flag", name)
			}

			settings[name] = true
		}

		for _, child := range cmd.Commands {
			walk(path+" "+child.Name, child)
		}
	}

	walk("forge-ci", root)

	for name, declared := range settings {
		if !declared {
			t.Errorf("the root does not declare --%s", name)
		}
	}
}

// A setting given on the command line reaches the gates of that invocation,
// at the root or after the subcommand, and the receipt names its source.
func TestAdminSettingReachesThePolicyOfItsInvocation(t *testing.T) {
	for _, args := range [][]string{
		{"forge-ci", "policy", "show", "--enforce-lint=false", "--enforce-reproducible-builds=true"},
		{"forge-ci", "--enforce-lint=false", "--enforce-reproducible-builds=true", "policy", "show"},
	} {
		shown := runShow(t, args)

		if shown.Settings["enforce-lint"] || shown.Settings["enforce-vulnerabilities"] || shown.Settings["enforce-application-tests"] || !shown.Settings["enforce-reproducible-builds"] {
			t.Errorf("%v: settings %v", args, shown.Settings)
		}

		want := map[string]string{
			"enforce-lint": "flag", "enforce-reproducible-builds": "flag",
			"enforce-vulnerabilities": "embedded-default", "enforce-application-tests": "embedded-default",
		}
		for name, source := range want {
			if shown.Sources[name] != source {
				t.Errorf("%v: %s source %q, want %q", args, name, shown.Sources[name], source)
			}
		}
	}

	for _, args := range [][]string{
		{"forge-ci", "policy", "show", "--enforce-lint=no"},
		{"forge-ci", "policy", "show", "--enforce-lint="},
		{"forge-ci", "policy", "show", "--enforce-lint=TRUE"},
		{"forge-ci", "--enforce-lint=true", "policy", "show", "--enforce-lint=false"},
		{"forge-ci", "policy", "show", "--enforce-lints=false"},
	} {
		root := cli.New(cli.BuildInfo{Version: "dev"})
		root.Writer, root.ErrWriter = io.Discard, io.Discard

		if err := root.Run(context.Background(), args); err == nil {
			t.Errorf("%v was admitted", args)
		}
	}
}

type shownPolicy struct {
	Settings map[string]bool   `json:"settings"`
	Sources  map[string]string `json:"sources"`
}

// runShow runs `policy show`, which prints to the process's stdout.
func runShow(t *testing.T, args []string) shownPolicy {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	stdout := os.Stdout
	os.Stdout = writer

	root := cli.New(cli.BuildInfo{Version: "dev"})
	root.Writer, root.ErrWriter = io.Discard, io.Discard
	runErr := root.Run(context.Background(), args)

	os.Stdout = stdout
	_ = writer.Close()

	body, _ := io.ReadAll(reader)

	if runErr != nil {
		t.Fatalf("%v: %v", args, runErr)
	}

	var shown shownPolicy
	if err := json.Unmarshal(body, &shown); err != nil || len(shown.Sources) != len(enforcement.Checks) {
		t.Fatalf("%v printed %q: %v", args, body, err)
	}

	return shown
}
