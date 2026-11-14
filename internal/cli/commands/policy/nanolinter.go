// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package policy

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/policycommand"
	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func nanolinterCmd() *cli.Command {
	return &cli.Command{
		Name: "nanolinter", Usage: "run full Nanolinter SARIF and independently gate lint and vulnerability findings",
		ArgsUsage: "[typed verify flags] or -- <nanolinter verify --report sarif --out - command>",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "sarif-file", Usage: "write the invocation's security-only SARIF for provider upload"},
			&cli.StringFlag{Name: "required-lints", Value: "secrets", Usage: "typed verify mode: mandatory check IDs; explicitly empty selects no floor"},
			&cli.StringFlag{Name: "disable-checks", Usage: "typed verify mode: check IDs excluded from this invocation"},
			&cli.StringFlag{Name: "offline", Value: "false", Usage: "typed verify mode: exactly true or false; true uses Nanolinter's offline checks"},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			typed := command.IsSet("required-lints") || command.IsSet("disable-checks") || command.IsSet("offline")
			if !typed {
				return appenforcement.Nanolinter(ctx, policycommand.Runner{}, os.Stdout, os.Stderr, command.Args().Slice(), command.String("sarif-file"))
			}

			if command.Args().Len() != 0 {
				return fmt.Errorf("typed Nanolinter verify flags cannot be combined with a child command: %w", errs.ErrUsage)
			}

			offline := command.String("offline")
			if offline != "true" && offline != "false" {
				return fmt.Errorf("offline must be true or false: %w", errs.ErrUsage)
			}

			return appenforcement.NanolinterVerify(ctx, policycommand.Runner{}, os.Stdout, os.Stderr, appenforcement.NanolinterVerifyInput{
				RequiredLints: command.String("required-lints"), DisableChecks: command.String("disable-checks"),
				Offline: offline == "true", SARIFFile: command.String("sarif-file"),
			})
		},
	}
}
