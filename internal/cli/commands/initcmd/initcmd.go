// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package initcmd wires `forge-ci init`: a repository's first Forge CI
// files, from the embedded examples, pinned to a Forge CI mirror.
package initcmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	appinit "github.com/diggsweden/forge-ci/internal/app/consumerinit"
	domain "github.com/diggsweden/forge-ci/internal/domain/consumerinit"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// New returns the `init` top-level command.
func New() *cli.Command {
	return &cli.Command{
		Name:      "init",
		Usage:     "write a repository's first Forge CI files from an example",
		ArgsUsage: "<forge-ci URL> [directory]",
		Description: `Copies the example for the repository's ecosystem and forge into place,
pinned to the newest release tag of the Forge CI repository the URL names.
The examples and the values its operator records in ` + domain.DefaultsFile + `
are read from that repository at that tag, so the files match the release
they pin. github.com is GitHub; any other host is the forge that file
names. Existing files are never overwritten. The report lists what is
still left to fill in.

EXAMPLES:
   # GitHub, from the published Forge CI:
   forge-ci init https://github.com/diggsweden/forge-ci

   # Forgejo or GitLab, from your organization's mirror:
   forge-ci init https://codefloe.com/itiquette/forge-ci

   # Another directory, a chosen example and tag:
   forge-ci init https://codefloe.com/itiquette/forge-ci ../hello --example go-service --ref v0.90.0`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "example", Usage: "example to start from, one of " + strings.Join(domain.Examples(), ", ") + " (default: from the repository's build files)"},
			&cli.StringFlag{Name: "ref", Usage: "release tag to pin (default: the newest vX.Y.Z tag of the Forge CI repository)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.NArg() < 1 || cmd.NArg() > 2 {
				return fmt.Errorf("init takes <forge-ci URL> [directory]: %w", errs.ErrUsage)
			}

			dir := "."
			if cmd.NArg() == 2 {
				dir = cmd.Args().Get(1)
			}

			example, err := chooseExample(cmd.String("example"), dir, os.Stdin, os.Stdout)
			if err != nil {
				return err
			}

			result, err := appinit.Run(ctx, appinit.Input{
				Remote: gitRemote{}, URL: cmd.Args().Get(0),
				Ref: cmd.String("ref"), Dir: dir, Example: example,
			})
			if err != nil {
				return err
			}

			report(os.Stdout, example, result)

			return nil
		},
	}
}

// chooseExample takes the flag, else the example the build files imply,
// else asks when a person is at the terminal.
func chooseExample(flag, dir string, in *os.File, out io.Writer) (string, error) {
	if flag != "" {
		if !slices.Contains(domain.Examples(), flag) {
			return "", fmt.Errorf("--example %q is not one of %s: %w", flag, strings.Join(domain.Examples(), ", "), errs.ErrUsage)
		}

		return flag, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	if example := domain.ExampleFor(projecttype.DetectFromEntries(names)); example != "" {
		return example, nil
	}

	if !term.IsTerminal(int(in.Fd())) { //nolint:gosec // a file descriptor fits an int.
		return "", fmt.Errorf("no build file tells the ecosystem; pass --example, one of %s: %w", strings.Join(domain.Examples(), ", "), errs.ErrUsage)
	}

	_, _ = fmt.Fprintf(out, "Example (%s) [go-cli]: ", strings.Join(domain.Examples(), ", "))

	line, _ := bufio.NewReader(in).ReadString('\n')
	if answer := strings.TrimSpace(line); answer != "" {
		return chooseExample(answer, dir, in, out)
	}

	return "go-cli", nil
}

func report(out io.Writer, example string, result appinit.Result) {
	values := ""
	if result.Defaults {
		values = ", values from " + domain.DefaultsFile
	}

	_, _ = fmt.Fprintf(out, "Forge CI %s (%.12s) from %s/%s: %s, example %s%s\n\nWrote\n",
		result.Source.Tag, result.Source.Commit, result.Source.Host, result.Source.Repository, result.Forge, example, values)

	for _, f := range result.Written {
		_, _ = fmt.Fprintf(out, "  %s\n", f)
	}

	if len(result.Remaining) == 0 {
		_, _ = fmt.Fprintln(out, "\nNothing left to fill in.")
	} else {
		_, _ = fmt.Fprintln(out, "\nStill to fill in")

		for _, f := range result.Written {
			for _, p := range result.Remaining[f] {
				_, _ = fmt.Fprintf(out, "  %s:%d  %s\n", f, p.Line, p.Token)
			}
		}
	}

	_, _ = fmt.Fprintf(out, "\nNext: the Getting started section of docs/%s.md for the secrets and variables, then forge-ci doctor.\n", result.Forge)
}

// gitRemote reads the mirror with git, anonymously.
type gitRemote struct{}

func (gitRemote) VersionTags(ctx context.Context, repoURL string) ([]string, error) {
	return git.New().RemoteVersionTags(ctx, repoURL, runcontext.Credential{})
}

func (gitRemote) TagCommit(ctx context.Context, repoURL, tag string) (string, error) {
	return git.New().RemoteTagCommit(ctx, repoURL, tag, runcontext.Credential{})
}

func (gitRemote) Export(ctx context.Context, repoURL, tag, dest string, paths []string) ([]string, error) {
	return git.ExportAtTag(ctx, repoURL, tag, dest, paths)
}
