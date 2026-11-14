// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan

import (
	"context"

	"github.com/urfave/cli/v3"

	appplan "github.com/diggsweden/forge-ci/internal/app/plan"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
)

func validateTestSelectionsCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate-test-selections",
		Usage: "validate complete administrator-owned Go/Cargo mappings against the release build-stage plan before fanout",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "build-stage-plan-json", Required: true},
			&cli.StringFlag{Name: "go-test-selections-json"},
			&cli.StringFlag{Name: "cargo-test-selections-json"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return appplan.ValidateReleaseTestSelections(ctx, d.OutputSink, cmd.String("build-stage-plan-json"), cmd.String("go-test-selections-json"), cmd.String("cargo-test-selections-json"))
			})
		},
	}
}
