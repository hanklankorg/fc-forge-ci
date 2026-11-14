// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
)

// ledgerMergeCmd wires `container ledger merge`; the use case is
// appcontainer.MergeLedgers.
func ledgerMergeCmd() *cli.Command {
	return &cli.Command{
		Name:      "merge",
		Usage:     "merge several per-container ledger files into one (drops exact duplicates); for promoting a multi-container release as a single unit",
		ArgsUsage: "<path>... (files, or directories searched for release-images.json)",
		Description: `EXAMPLE:
   forge-ci container ledger merge dist/container-a dist/container-b --ledger release-images.json`,
		Flags: []cli.Flag{
			ledgerPathFlag(),
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return appcontainer.MergeLedgers(cmd.Args().Slice(), cmd.String(flagLedger), os.Stderr)
		},
	}
}
