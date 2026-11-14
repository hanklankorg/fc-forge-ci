// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

func releaseImagesImportCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdImport,
		Usage: "copy unsigned release-image digests to a separate publication repository and write its ledger",
		Flags: append(releaseImagesCommonFlags(),
			&cli.StringFlag{Name: "destination-repository", Required: true, Usage: "exact publication image repository, without tag or digest"},
			&cli.StringFlag{Name: "output-ledger", Required: true, Usage: "new publication ledger under --dist-dir; the input ledger is preserved"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			common, err := releaseImagesCommonFromCmd(cmd)
			if err != nil {
				return err
			}

			if stageErr := requireReleaseStage(cmd.String("stage")); stageErr != nil {
				return stageErr
			}

			output := cmd.String("output-ledger")
			if pathErr := appcontainer.ValidateReleaseImagesPath(output, common.DistDir); pathErr != nil {
				return pathErr
			}

			if filepath.Clean(output) == filepath.Clean(common.Ledger) {
				return fmt.Errorf("release images: output-ledger must preserve the input ledger: %w", errs.ErrUsage)
			}

			entries, _, err := common.admitLedger(cmd, true)
			if err != nil {
				return err
			}

			destination := cmd.String("destination-repository")
			if _, err := imageledger.PlanReleaseImport(entries, common.ReleaseTag, common.ExpectedImageRepository, destination); err != nil {
				return err
			}

			return withReleaseImagesDockerConfig(func(authFile string) error {
				if err := common.login(authFile); err != nil {
					return err
				}

				imported, err := imageledger.ImportReleaseImages(ctx, ociregistry.WithAuthFile(authFile), entries,
					common.ReleaseTag, common.ExpectedImageRepository, destination)
				if err != nil {
					return err
				}

				body, err := imageledger.Marshal(imported)
				if err != nil {
					return err
				}

				return cliio.WriteFile(output, body, 0o644)
			})
		},
	}
}
