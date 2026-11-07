// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/urfave/cli/v3"
	"os"
)

func filesCompareCmd() *cli.Command {
	return &cli.Command{Name: "compare", Usage: "compare exact reproducible release subjects with caller-owned expected inventory",
		Flags: []cli.Flag{&cli.StringFlag{Name: "reference", Required: true}, &cli.StringFlag{Name: flagDistDir, Required: true}, &cli.StringSliceFlag{Name: "expected", Required: true, Usage: "one glob per required subject"}, &cli.StringSliceFlag{Name: "inventory-pattern", Required: true, Usage: "glob selecting candidate subjects, including unexpected extras"}},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if err := apprelease.CompareReleaseFiles(cmd.String("reference"), cmd.String(flagDistDir), cmd.StringSlice("expected"), cmd.StringSlice("inventory-pattern")); err != nil {
				return err
			}

			_, err := fmt.Fprintf(os.Stdout, "%d release files are byte-identical\n", len(cmd.StringSlice("expected")))

			return err
		},
	}
}
