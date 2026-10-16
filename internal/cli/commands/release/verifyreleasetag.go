// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/clitoken"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// verifyTagCmd is the Go port of forgejo-ci's verify-release-tag.sh: a
// trust-boundary re-check that the checkout, the published remote tag,
// and "this is still the latest release" all hold before signing —
// refusing to publish a stale or hijacked release.
func verifyTagCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate-tag",
		Usage: "re-validate the release tag against the remote: checkout==release-sha, annotated tag object matches, tag points to it, and not superseded",
		Description: `EXAMPLE:
   forge-ci release validate-tag --tag v1.2.3 --release-sha abc... --repo-url https://github.com/org/app`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagReleaseSHA, Sources: cli.EnvVars("RELEASE_SHA"), Usage: "commit the release is built from"},
			&cli.StringFlag{Name: flagTag, Sources: cienv.Tag(), Usage: "release tag (e.g. v1.2.3)"},
			&cli.StringFlag{Name: "repo-url", Sources: cli.EnvVars("REPO_URL"), Usage: "remote repository URL for ls-remote (defaults to origin)"},
			&cli.StringFlag{Name: "expected-parent", Sources: cli.EnvVars("RELEASE_REQUEST_SHA"), Usage: "require the release commit to be the single child of this commit (the authorised release-request source)"},
			&cli.BoolFlag{Name: "changelog-only", Usage: "require exactly a CHANGELOG.md change from --expected-parent, and bind release-request/<tag> to that tested commit"},
			&cli.StringFlag{Name: "version-bump", Usage: "with --changelog-only, also admit this project type's version bump (maven), checked as a version substitution to the tag's version"},
			clitoken.FileFlag("Read token for a private repository."),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			token, err := clitoken.Resolve(cmd)
			if err != nil {
				return err
			}

			if cmd.String("version-bump") != "" && !cmd.Bool("changelog-only") {
				return fmt.Errorf("--version-bump only widens --changelog-only: %w", errs.ErrUsage)
			}

			return apprelease.VerifyReleaseTag(ctx, git.New(), os.Stderr, apprelease.VerifyReleaseTagInput{
				ReleaseSHA:     cmd.String(flagReleaseSHA),
				Tag:            cmd.String(flagTag),
				RepoURL:        cmd.String("repo-url"),
				ExpectedParent: cmd.String("expected-parent"),
				ChangelogOnly:  cmd.Bool("changelog-only"),
				VersionBump:    projecttype.Type(cmd.String("version-bump")),
				Token:          token,
			})
		},
	}
}
