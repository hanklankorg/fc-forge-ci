// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

func notesCmd() *cli.Command {
	return &cli.Command{
		Name:  "notes",
		Usage: "prepare release notes from an artifact or an exact changelog version section",
		Description: `EXAMPLE:
   forge-ci release notes --source-file CHANGELOG.md --target-file release-notes.md --release-version v1.2.3`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "changelog-file", Usage: "extract the exact --release-version section from this Keep a Changelog file; a missing or duplicate section fails instead of producing a stub"},
			&cli.StringFlag{Name: "source-file", Value: "ReleasenotesTmp", Sources: cli.EnvVars("SOURCE_FILE"), Usage: "path to the git-cliff–generated changelog used as the source body"},
			&cli.StringFlag{Name: "target-file", Value: domainrelease.DefaultReleaseNotesFile, Sources: cli.EnvVars("TARGET_FILE"), Usage: "destination path for the assembled release-notes body (\"-\" writes to stdout)"},
			&cli.StringFlag{Name: "release-version", Sources: cli.EnvVars("RELEASE_VERSION"), Usage: "version to extract with --changelog-file, or use in the fallback header when no source file is found"},
			&cli.StringFlag{Name: "release-commit", Sources: cli.EnvVars("RELEASE_COMMIT"), Usage: "release commit SHA used in the fallback body"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.IsSet("changelog-file") && cmd.IsSet("source-file") {
				return fmt.Errorf("release notes: --changelog-file and --source-file are separate source modes: %w", errs.ErrUsage)
			}

			return apprelease.PrepareNotes(ctx, os.Stderr, apprelease.PrepareNotesInput{
				SourceFile:     cmd.String("source-file"),
				ChangelogFile:  cmd.String("changelog-file"),
				TargetFile:     cmd.String("target-file"),
				ReleaseVersion: cmd.String("release-version"),
				ReleaseCommit:  cmd.String("release-commit"),
			})
		},
	}
}

func verifyChangelogCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate-changelog",
		Usage: "validate the generated changelog artifact for this release exists and print a preview",
		Description: `EXAMPLE:
   forge-ci release validate-changelog --changelog-file CHANGELOG.md`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "changelog-file",
				Required: true,
				Sources:  cli.EnvVars("CHANGELOG_FILE"),
				Usage:    "path to the generated changelog file to verify",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return apprelease.VerifyChangelog(ctx, os.Stderr, apprelease.VerifyChangelogInput{
				ChangelogFile: cmd.String("changelog-file"),
			})
		},
	}
}
