// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	urfavecli "github.com/urfave/cli/v3"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// TestGroupsFailClosedWithoutASubcommand asserts that every group command
// (one carrying subcommands) refuses to run bare, with a usage error → exit 2.
//
// urfave/cli's default is to print help and exit 0, which is fail-OPEN: a
// workflow step that lost its last path segment (`forge-ci validate tag`
// where `validate tag signature` was meant) would pass green having validated
// nothing. Nothing else in the guard family catches that — the verb path still
// exists, so forgejo-ci's test-cli-invocation-contract.sh is satisfied, and the
// surface is unchanged, so the reference-sync and help guards are satisfied. It
// is a runtime behaviour, and every other check here inspects surface facts.
//
// Walking the live tree means a newly added group inherits the invariant
// automatically: forget to give it subcommands-or-an-Action and this fails.
func TestGroupsFailClosedWithoutASubcommand(t *testing.T) {
	t.Parallel()

	root := cli.New(cli.BuildInfo{Version: "dev"})

	var offenders []string

	var walk func(path string, cmd *urfavecli.Command)

	walk = func(path string, cmd *urfavecli.Command) {
		for _, sub := range cmd.Commands {
			walk(path+" "+sub.Name, sub)
		}

		// Leaves are the arg-guard's business (see applyArgGuards), not this one.
		if len(cmd.Commands) == 0 {
			return
		}

		if cmd.Action == nil {
			offenders = append(offenders,
				path+": group has no Action — urfave/cli would print help and exit 0 (fail-open)")

			return
		}

		err := cmd.Action(context.Background(), cmd)
		if !errors.Is(err, errs.ErrUsage) {
			offenders = append(offenders, fmt.Sprintf(
				"%s: bare group returned %v — want an errs.ErrUsage (exit 2)", path, err))
		}
	}

	for _, sub := range root.Commands {
		walk(sub.Name, sub)
	}

	require.Emptyf(t, offenders, "groups that do not fail closed:\n%s", strings.Join(offenders, "\n"))
}

// TestBareGroupErrorNamesEveryChild requires each group's refusal to list
// exactly its invocable children — every one, and nothing else.
//
// It used to check a single group for a single name, which a diagnostic
// listing one stale subcommand satisfies. The list is the whole remedy: the
// operator typed a group and has to be told what completes it, and a CI log
// line is often all they get. A subcommand added without reaching this list is
// undiscoverable from the failure it causes, and a name left in it after a
// rename sends them to a command that no longer exists.
//
// Walking the live tree means a new group inherits the check rather than
// needing its own case.
//
// There is deliberately no assertion that "help" is absent from the list. The
// test this replaces had one, and it could not fail: urfave/cli v3 serves help
// through a flag rather than a child command, so no node in the built tree has
// a child named "help" — which is also why the skip that used to be in
// subcommandNames is gone.
func TestBareGroupErrorNamesEveryChild(t *testing.T) {
	t.Parallel()

	root := cli.New(cli.BuildInfo{Version: "dev"})

	var walk func(path string, cmd *urfavecli.Command)

	walk = func(path string, cmd *urfavecli.Command) {
		for _, sub := range cmd.Commands {
			walk(path+" "+sub.Name, sub)
		}

		if len(cmd.Commands) == 0 || cmd.Action == nil {
			return
		}

		// Only groups whose Action is the guard's refusal are in scope; a
		// group with real work of its own is not making this promise.
		err := cmd.Action(context.Background(), cmd)
		if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "requires a subcommand") {
			return
		}

		msg := err.Error()

		var want []string

		for _, child := range cmd.Commands {
			want = append(want, child.Name)

			if !strings.Contains(msg, child.Name) {
				t.Errorf("%s: the refusal does not name the subcommand %q:\n%s", path, child.Name, msg)
			}
		}

		// Exactly those: an extra name in the list is a command that does not
		// exist, which is worse than an omission because the operator will try
		// it. The listed set is recovered from the message's own parenthesis.
		_, listed, found := strings.Cut(msg, "(one of: ")
		if !found {
			t.Errorf("%s: the refusal has no subcommand list:\n%s", path, msg)

			return
		}

		listed, _, _ = strings.Cut(listed, ")")

		got := strings.Split(listed, ", ")
		slices.Sort(got)
		slices.Sort(want)

		if !slices.Equal(got, want) {
			t.Errorf("%s: refusal lists %v, want exactly %v", path, got, want)
		}
	}

	for _, sub := range root.Commands {
		walk(sub.Name, sub)
	}
}

// TestRootBareInvocationIsAUsageErrorWithACard: `forge-ci` alone is no longer
// the one exemption that printed the reference and exited 0. Like its
// siblings, it writes a short start card to stderr and refuses with a usage
// error, so a step that lost every path segment fails like one that lost its
// last. --help and `help` remain the explicit, exit-0 requests for the
// reference (cmd/forge-ci smoke tests).
func TestRootBareInvocationIsAUsageErrorWithACard(t *testing.T) {
	t.Parallel()

	root := cli.New(cli.BuildInfo{Version: "dev"})

	var stdout, stderr bytes.Buffer

	root.Writer = &stdout
	root.ErrWriter = &stderr

	err := root.Run(context.Background(), []string{"forge-ci"})
	require.ErrorIs(t, err, cli.ErrNoCommand)
	require.ErrorIs(t, err, errs.ErrUsage)
	require.Empty(t, stdout.String(), "the card is a refusal and belongs on stderr")

	card := stderr.String()
	require.Contains(t, card, "START HERE")
	require.Contains(t, card, "forge-ci init")
	require.Contains(t, card, "forge-ci doctor")
	require.Contains(t, card, "--help")

	// The house budget for brief help is about fifteen lines; the card is the
	// brief help, so it stays near that, not the sixty-line reference.
	require.LessOrEqual(t, strings.Count(card, "\n"), 18, "card grew past the brief-help budget:\n%s", card)
}

// TestRootCardDocsLinkUsesTheBareTag: the card's Docs link must come from the
// bare version, not cmd.Version, which is the composed "v0.90.0 (commit …,
// built …)" string. Dev builds hide the difference (both fall back to main),
// so this runs a release-shaped BuildInfo.
func TestRootCardDocsLinkUsesTheBareTag(t *testing.T) {
	t.Parallel()

	root := cli.New(cli.BuildInfo{Version: "v0.90.0", Commit: "abc1234", Date: "2026-10-31"})

	var stdout, stderr bytes.Buffer

	root.Writer = &stdout
	root.ErrWriter = &stderr

	require.ErrorIs(t, root.Run(context.Background(), []string{"forge-ci"}), cli.ErrNoCommand)
	require.Contains(t, stderr.String(), "/tree/v0.90.0/docs")
	require.NotContains(t, stderr.String(), "(commit")
}

// TestRootUnknownSubcommandKeepsItsSuggestion: giving the root an Action takes
// over urfave/cli's unknown-command path, so the "did you mean" line is now
// this project's to keep. The wording is the one main.go has always printed.
func TestRootUnknownSubcommandKeepsItsSuggestion(t *testing.T) {
	t.Parallel()

	root := cli.New(cli.BuildInfo{Version: "dev"})

	var sink bytes.Buffer

	root.Writer = &sink
	root.ErrWriter = &sink

	err := root.Run(context.Background(), []string{"forge-ci", "relese"})
	require.ErrorIs(t, err, errs.ErrUsage)
	require.Equal(t, `unknown subcommand 'relese'. Did you mean "release"?: usage error`, err.Error())
	require.Empty(t, sink.String(), "an unknown subcommand is reported by main.go, not printed here")
}

// TestBareGroupsRefuseThroughRootRun drives the refusal through the parser
// instead of calling the Action directly.
//
// TestGroupsFailClosedWithoutASubcommand walks the command tree and invokes
// each group's Action itself. That proves the Action refuses; it does not prove
// that typing the bare group REACHES it. Those differ whenever the framework
// decides otherwise — a group that gained a default subcommand, an Action
// shadowed by a flag handler, a name that no longer resolves — and the failure
// mode is the one the guard exists to prevent: a step that ran nothing and
// exited 0.
//
// The streams are captured too. The refusal must arrive as an error for the
// caller to classify, not as help text printed to stdout, because a workflow
// reads the exit code and a green exit with a help page is exactly the
// fail-open shape being guarded against.
func TestBareGroupsRefuseThroughRootRun(t *testing.T) {
	for _, group := range []string{"validate", "container", "release", "build", "publish"} {
		t.Run(group, func(t *testing.T) {
			// Not parallel: the run is given an owned working directory so
			// "left nothing behind" is checkable.
			dir := t.TempDir()
			t.Chdir(dir)

			root := cli.New(cli.BuildInfo{Version: "dev"})

			var stdout, stderr bytes.Buffer

			root.Writer = &stdout
			root.ErrWriter = &stderr

			err := root.Run(context.Background(), []string{"forge-ci", group})
			if !errors.Is(err, errs.ErrUsage) {
				t.Fatalf("bare %q returned %v, want ErrUsage (exit 2)", group, err)
			}

			// Actionable: the operator gets the subcommands they could have
			// typed, in the error itself.
			if !strings.Contains(err.Error(), group) {
				t.Errorf("error does not name the group: %v", err)
			}

			// urfave/cli appends a "help" child to every group at Run time;
			// it is not an operation and must not be offered as one.
			if strings.Contains(err.Error(), " help,") || strings.Contains(err.Error(), " help)") {
				t.Errorf("bare %q offers the built-in help child: %v", group, err)
			}

			// Nothing is printed as if the command had succeeded.
			if stdout.Len() != 0 {
				t.Errorf("bare %q wrote to stdout: %q", group, stdout.String())
			}

			// A refusal touches no filesystem state.
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}

			if len(entries) != 0 {
				names := make([]string, 0, len(entries))
				for _, e := range entries {
					names = append(names, e.Name())
				}

				t.Errorf("bare %q left %v in the working directory", group, names)
			}
		})
	}
}
