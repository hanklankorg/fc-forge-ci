// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"

	"github.com/urfave/cli/v3"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
)

func gatherExportsCmd() *cli.Command {
	return &cli.Command{Name: "gather-exports", Usage: "validate and join pre-manifest export trees without overwriting any top-level path",
		Flags: []cli.Flag{&cli.StringSliceFlag{Name: "source", Required: true}, &cli.StringFlag{Name: flagPath, Required: true}, &cli.BoolFlag{Name: "allow-missing"}, &cli.StringFlag{Name: "enabled", Value: "true"}},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			enabled, err := parseStrictBoolFlag(cmd.String("enabled"), "enabled")
			if err != nil {
				return err
			}

			return apprelease.GatherExports(ctx, apprelease.GatherExportsInput{Sources: cmd.StringSlice("source"), Destination: cmd.String(flagPath), Enabled: enabled, AllowMissing: cmd.Bool("allow-missing")})
		},
	}
}
