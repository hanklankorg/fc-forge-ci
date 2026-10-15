// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/urfave/cli/v3"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func assetRetentionCmd() *cli.Command {
	return &cli.Command{
		Name:  "asset-retention",
		Usage: "delete the large files of releases older than the newest N, keeping their checksums and signatures",
		Description: `Keeps every file of the newest --keep published releases. Each older
release keeps the files --keep-files matches, by default its checksum
manifests, signatures, bundles and SBOMs, and loses the rest: archives,
binaries and packages. The releases, their notes and their tags stay.

With reproducible builds a deleted file can be rebuilt from its tag and
checked against the kept checksums, and against its own signature.

Drafts are neither counted nor touched; an immutable GitHub release is
counted and kept whole. On GitLab, whose release files are project uploads
behind links, the upload is deleted with its link.

--dry-run is the default: run it, read the list, then pass --dry-run=false.

EXAMPLE:
   forge-ci release asset-retention --repository examplescope/myapp --keep 6`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagRepository, Required: true, Sources: cienv.Repository(), Usage: "\"owner/repo\" whose releases to trim"},
			&cli.IntFlag{Name: "keep", Required: true, Sources: cli.EnvVars("RELEASE_ASSET_RETENTION"), Usage: "how many of the newest published releases keep every file"},
			&cli.StringFlag{Name: "keep-files", Value: apprelease.DefaultAssetRetentionKeepFiles, Sources: cli.EnvVars("RELEASE_ASSET_RETENTION_KEEP_FILES"), Usage: "regular expression of the file names older releases keep"},
			&cli.IntFlag{Name: "max-delete", Value: 200, Sources: cli.EnvVars("RELEASE_ASSET_RETENTION_MAX_DELETE"), Usage: "refuse a pass that would delete more files than this (0 disables the bound)"},
			&cli.BoolFlag{Name: "dry-run", Value: true, Sources: cli.EnvVars("RELEASE_ASSET_RETENTION_DRY_RUN"), Usage: "report what would be deleted without deleting it"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			keepFiles, err := regexp.Compile(cmd.String("keep-files"))
			if err != nil {
				return fmt.Errorf("--keep-files: %w: %w", err, errs.ErrUsage)
			}

			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				pruner, err := d.RequireReleaseAssetPruner()
				if err != nil {
					return err
				}

				_, err = apprelease.ApplyAssetRetention(ctx, pruner, os.Stdout, apprelease.AssetRetentionInput{
					Repository: cmd.String(flagRepository),
					Keep:       cmd.Int("keep"),
					KeepFiles:  keepFiles,
					MaxDelete:  cmd.Int("max-delete"),
					DryRun:     cmd.Bool("dry-run"),
				})

				return err
			})
		},
	}
}
