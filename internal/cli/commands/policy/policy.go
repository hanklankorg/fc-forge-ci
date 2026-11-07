// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package policy wires admin policy inspection and owned check gates.
package policy

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/diggsweden/forge-ci/internal/adapters/policycommand"
	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/output"
)

// show prints the policy as text to a person at a terminal, and otherwise as
// the JSON receipt the CI jobs log and tests read.
func show(ctx context.Context, cmd *cli.Command) error {
	format, err := deps.OutputFormat(cmd)
	if err != nil {
		return err
	}

	if format != output.FormatJSON && term.IsTerminal(int(os.Stdout.Fd())) {
		return appenforcement.ShowText(ctx, os.Stdout)
	}

	return appenforcement.Show(ctx, os.Stdout)
}

// New returns the admin policy command group.
func New() *cli.Command {
	return &cli.Command{Name: "policy", Usage: "administrator check policy: inspect it and run gates under it", Commands: []*cli.Command{
		{Name: "show", Usage: "print the effective admin settings, the source of each (embedded-default or flag) and the embedded default's SHA-256 (default-sha256)", Action: show},
		nanolinterCmd(),
		{Name: "tests", Usage: "run application tests and apply admin enforcement to recognized assertion failures", ArgsUsage: "-- <native test command>", Flags: []cli.Flag{
			&cli.StringFlag{Name: "protocol", Required: true, Usage: "native report protocol: go-json, maven, cargo, node, gradle, xctest"},
		}, Action: func(ctx context.Context, command *cli.Command) error {
			return appenforcement.Tests(ctx, policycommand.Runner{}, os.Stdout, os.Stderr, enforcement.TestProtocol(command.String("protocol")), command.Args().Slice())
		}},
	}}
}
