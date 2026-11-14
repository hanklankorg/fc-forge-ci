// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform

import (
	"context"
	"fmt"
	"os"

	appplatform "github.com/diggsweden/forge-ci/internal/app/platform"
	"github.com/urfave/cli/v3"
)

func dependencyCmd() *cli.Command {
	return &cli.Command{
		Name: "dependency", Usage: "admit one exact or namespace-qualified job from serialized workflow needs",
		Description: "Requires exactly one matching local job ID and an admitted result (success by default). A requested output must be a nonempty single-line string. No provider calls or workflow expressions are evaluated. Use this in an always-running facts job before reusable calls consume its outputs.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "needs-json", Sources: cli.EnvVars("NEEDS_JSON"), Required: true, Usage: "serialized workflow needs object"},
			&cli.StringFlag{Name: "job", Required: true, Usage: "local job ID, with no namespace prefix"},
			&cli.StringSliceFlag{Name: "result", Usage: "admitted result (repeatable); defaults to success"},
			&cli.StringFlag{Name: "output", Usage: "return this string output instead of the dependency JSON object"},
			&cli.BoolFlag{Name: "allow-missing-output", Usage: "allow absent/empty output after result admission; only for explicitly optional paths"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			value, err := appplatform.Dependency(appplatform.DependencyInput{NeedsJSON: cmd.String("needs-json"), Job: cmd.String("job"), Results: cmd.StringSlice("result"), Output: cmd.String("output"), AllowMissingOutput: cmd.Bool("allow-missing-output")})
			if err != nil {
				return err
			}

			_, err = fmt.Fprintln(os.Stdout, value)

			return err
		},
	}
}
