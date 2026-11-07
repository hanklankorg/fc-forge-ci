// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package setup wires `forge-ci setup`: the checklist from a repository's
// first Forge CI files to its first release, and the commands for the steps
// in it that a person does by hand.
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/app/releasesetup"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
)

// New returns the `setup` top-level command. commit is the binary's build
// commit, which the secrets report uses as `doctor --requirements` does.
func New(commit string) *cli.Command {
	return &cli.Command{
		Name:  "setup",
		Usage: "show what is left before a first release, and do the manual steps (signers, secrets, release request)",
		Description: `Start with forge-ci setup status: it lists the steps from no Forge CI files
to a first release, marks the ones the repository shows are done, and names
the command for each. The other subcommands do the steps a person does by hand.

EXAMPLES:
   # Where am I?
   forge-ci setup status

   # Trust your key and the release bot's key for signed release tags:
   forge-ci setup signer --principal you@example.org --public-key ~/.ssh/id_ed25519.pub
   forge-ci setup signer --principal release-bot@example.org --public-key release-bot.pub

   # Which secrets to create, and the command that stores each:
   forge-ci setup secrets

   # Check your signing setup and print the commands that request v1.0.0:
   forge-ci setup release-request v1.0.0`,
		// No Action: like every group, a bare `setup` refuses with the list
		// of its subcommands (see applyParentGuards).
		Commands: []*cli.Command{statusCmd(commit), signerCmd(), secretsCmd(commit), requestCmd()},
	}
}

func statusCmd(commit string) *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "list the steps to a first release, which are done, and the command for each",
		Description: `Reads the same checks as forge-ci doctor, so the two agree about what is
done. Steps the repository cannot show (secrets on the forge, a merged pull
request) are marked [?] for you to check.`,
		Flags: []cli.Flag{rootFlag()},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			steps, err := releasesetup.Checklist(ctx, cmd.String("root"), "", commit)
			if err != nil {
				return err
			}

			return render(cmd, steps, func(out io.Writer) { releasesetup.FormatChecklist(out, steps) })
		},
	}
}

func rootFlag() cli.Flag {
	return &cli.StringFlag{Name: "root", Value: ".", Usage: "repository root"}
}

func signerCmd() *cli.Command {
	return &cli.Command{
		Name:  "signer",
		Usage: "add an SSH public key to the allowed-signers file the release verifies signed tags against",
		Description: `Appends "<principal> <key-type> <key>" to the repository's allowed-signers
file, creating it when needed, and validates it. Add the key of each person
who requests releases, and the release bot's key (the public half of
RELEASE_SSH_SIGNING_KEY) with the bot-email of the release workflow as its
principal. Commit the file; the release reads it from the default branch.

The file is .forge-ci/allowed_signers, or .forgejo/release-allowed-signers in
a repository with only Forgejo workflows (the Forgejo examples' default).
GitLab takes the text as the request-allowed-signers input: use --dry-run and
paste the line.

To make a key for the release bot:
   ssh-keygen -t ed25519 -C release-bot -N '' -f release-bot
   (store the private half, release-bot, as RELEASE_SSH_SIGNING_KEY; then delete it)`,
		Flags: []cli.Flag{
			rootFlag(),
			&cli.StringFlag{Name: "principal", Required: true, Usage: "the signer's email: your git user.email, or the release workflow's bot-email"},
			&cli.StringFlag{Name: "public-key", Required: true, Usage: "OpenSSH public key file (.pub)"},
			&cli.StringFlag{Name: "file", Usage: "allowed-signers file relative to --root (default: as described above)"},
			&cli.BoolFlag{Name: "dry-run", Usage: "print the line without writing it"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			result, err := releasesetup.AddSigner(releasesetup.SignerInput{
				Root: cmd.String("root"), File: cmd.String("file"), Principal: cmd.String("principal"),
				PublicKeyPath: cmd.String("public-key"), DryRun: cmd.Bool("dry-run"),
			})
			if err != nil {
				return err
			}

			out := writer(cmd)

			switch {
			case result.Present:
				_, _ = fmt.Fprintf(out, "%s already lists this key for %s; nothing changed.\n", result.File, cmd.String("principal"))
			case cmd.Bool("dry-run"):
				_, _ = fmt.Fprintln(out, result.Line)
			default:
				verb := "Added to"
				if result.Created {
					verb = "Created"
				}

				_, _ = fmt.Fprintf(out, "%s %s:\n  %s\n\nNext: commit %s; the release reads it from the default branch.\n", verb, result.File, result.Line, result.File)
			}

			return nil
		},
	}
}

func secretsCmd(commit string) *cli.Command {
	return &cli.Command{
		Name:  "secrets",
		Usage: "list the secrets the release workflows need, what each must allow, and the command that stores it",
		Description: `Reads the release workflows in the repository, like forge-ci doctor
--requirements, and lists the credentials to create on the forge. It cannot
see the forge's secrets, so it says what to create, not what is missing.`,
		Flags: []cli.Flag{rootFlag()},
		Action: func(_ context.Context, cmd *cli.Command) error {
			report := releasesetup.Secrets(cmd.String("root"), "", commit)

			return render(cmd, report, func(out io.Writer) { releasesetup.FormatSecrets(out, report) })
		},
	}
}

func requestCmd() *cli.Command {
	return &cli.Command{
		Name:      "release-request",
		Usage:     "check your signing setup for a release request and print the git commands that make it",
		ArgsUsage: "<version>",
		Description: `A release starts when a signed release-request/vX.Y.Z tag is pushed; the
release workflow, not you, then creates vX.Y.Z. This checks that git signs
tags with an SSH key the repository's allowed-signers file lists, and that
neither tag exists, then prints the two git commands. It does not run them.
It exits 1 while a check fails.`,
		Flags: []cli.Flag{
			rootFlag(),
			&cli.StringFlag{Name: "remote", Value: "origin", Usage: "git remote to push the request tag to"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.NArg() != 1 {
				return fmt.Errorf("release-request takes one version, for example v1.2.3: %w", errs.ErrUsage)
			}

			root := cmd.String("root")

			plan, err := releasesetup.PlanRequest(ctx, releasesetup.RequestInput{
				Root: root, Version: cmd.Args().First(), Remote: cmd.String("remote"), Git: &git.Repo{Dir: root},
			})
			if err != nil {
				return err
			}

			if err := render(cmd, plan, func(out io.Writer) { releasesetup.FormatRequest(out, plan) }); err != nil {
				return err
			}

			if !plan.Ready() {
				return fmt.Errorf("release request for %s is not ready; see the [todo] lines: %w", plan.Version, errs.ErrValidation)
			}

			return nil
		},
	}
}

// render writes v as JSON under the global --json/--format=json, else the
// text form; an invalid --format is an error, as for every other command.
func render(cmd *cli.Command, v any, text func(io.Writer)) error {
	format, err := deps.OutputFormat(cmd)
	if err != nil {
		return err
	}

	if format == output.FormatJSON {
		return writeJSON(writer(cmd), v)
	}

	text(writer(cmd))

	return nil
}

// writer is the command's output, or stdout for a command run outside
// urfave/cli's Run (a test calling an Action directly).
func writer(cmd *cli.Command) io.Writer {
	if cmd.Writer != nil {
		return cmd.Writer
	}

	return os.Stdout
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")

	return enc.Encode(v)
}
