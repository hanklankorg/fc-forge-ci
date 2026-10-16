// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// applyBumpCmd wires `forge-ci version apply-bump`. Every flag is explicit and
// has no environment source: each names a trust decision of the calling job.
func applyBumpCmd() *cli.Command {
	return &cli.Command{
		Name:  "apply-bump",
		Usage: "verify a version-bump patch from a key-free job and apply it, without running a build tool",
		Description: `The patch must match its reported sha256, change only the project files of
the type (pom.xml for maven), and change nothing but the root project's
current version into --version. The changed paths are written to --paths-file
for commit-changelog-release --include-paths-file.

EXAMPLE:
   forge-ci version apply-bump --project-type maven --version 1.2.3 \
     --patch version-bump.patch --sha256 <hex> --paths-file bump-paths.txt`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "patch", Required: true, Usage: "git diff the bump job uploaded"},
			&cli.StringFlag{Name: "sha256", Required: true, Usage: "sha256 the bump job reported for the patch"},
			&cli.StringFlag{Name: flagProjectType, Required: true, Usage: "ecosystem whose project files the patch may change (maven)"},
			&cli.StringFlag{Name: flagVersion, Required: true, Usage: "release version, with or without a leading v"},
			&cli.StringFlag{Name: "working-dir", Value: ".", Usage: "repository-relative directory of the root project file"},
			&cli.StringFlag{Name: "paths-file", Required: true, Usage: "file the changed paths are written to, one per line"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			_, err := appversion.ApplyBump(ctx, git.New(), os.Stderr, appversion.ApplyBumpInput{
				PatchPath:   cmd.String("patch"),
				SHA256:      cmd.String("sha256"),
				ProjectType: projecttype.Type(cmd.String(flagProjectType)),
				Version:     cmd.String(flagVersion),
				WorkingDir:  cmd.String("working-dir"),
				PathsFile:   cmd.String("paths-file"),
			})

			return err
		},
	}
}
