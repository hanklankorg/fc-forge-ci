// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/urfave/cli/v3"

	appplan "github.com/diggsweden/forge-ci/internal/app/plan"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func releaseExportsCmd() *cli.Command {
	return &cli.Command{Name: "release-exports", Usage: "admit native export jobs or print whether an architecture is selected",
		Flags: []cli.Flag{&cli.StringFlag{Name: "enabled", Value: "false"}, &cli.StringFlag{Name: "job-image"}, &cli.StringFlag{Name: "release-platforms"}, &cli.StringFlag{Name: flagARM64Tags, Value: "[]"}, &cli.StringFlag{Name: "commands-file", Value: "-", Usage: "JSON export commands, data only"}, &cli.StringFlag{Name: "image-plan", Usage: "optional release image plan file"}, &cli.StringFlag{Name: "selected-arch", Usage: "print true/false for this native architecture instead of admitting export jobs"}},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if arch := cmd.String("selected-arch"); arch != "" {
				if arch != "amd64" && arch != "arm64" {
					return fmt.Errorf("unknown export architecture: %w", errs.ErrUsage)
				}

				platforms, err := appplan.ReleaseExportPlatforms(cmd.String("release-platforms"))
				if err != nil {
					return err
				}

				_, err = fmt.Fprintln(os.Stdout, slices.Contains(platforms, "linux/"+arch))

				return err
			}

			commands, err := cliio.ReadFile(cmd.String("commands-file"))
			if err != nil {
				return err
			}

			in := appplan.ReleaseExportsInput{Enabled: cmd.String("enabled"), JobImage: cmd.String("job-image"), Platforms: cmd.String("release-platforms"), ARM64Tags: cmd.String(flagARM64Tags), Commands: string(commands)}
			if cmd.String("image-plan") != "" {
				in.ImagePlan, err = cliio.ReadRegularFile(cmd.String("image-plan"))
				if err != nil {
					return err
				}
			}

			return appplan.CheckReleaseExports(in)
		},
	}
}
