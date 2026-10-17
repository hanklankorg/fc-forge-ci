// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/changelog"
	"github.com/diggsweden/forge-ci/internal/adapters/git"
	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/cli/clitoken"
	"github.com/diggsweden/forge-ci/internal/cli/secretenv"
)

func renderChangelogCmd() *cli.Command {
	return &cli.Command{
		Name:  "render-changelog",
		Usage: "render CHANGELOG.md and commit-msg.txt with git-chglog or git-cliff",
		Description: `EXAMPLE:
   forge-ci version render-changelog --backend git-cliff --tag v1.2.3 \
     --repository-url https://forgejo.example/owner/repository \
     --changelog-config .forge-ci/gitcliff/keepachangelog.toml \
     --commit-body-config .forge-ci/gitcliff/minimal.toml`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "backend", Value: "git-cliff", Sources: cli.EnvVars("CHANGELOG_BACKEND"), Usage: "changelog renderer backend: git-cliff or git-chglog"},
			&cli.StringFlag{Name: flagTag, Required: true, Sources: cli.EnvVars("RELEASE_TAG", "TAG_NAME"), Usage: "final stable release tag (vMAJOR.MINOR.PATCH)"},
			&cli.StringFlag{Name: flagRepositoryURL, Required: true, Sources: cli.EnvVars("CHANGELOG_REPOSITORY_URL"), Usage: "canonical HTTPS web URL of the repository used for generated changelog links"},
			&cli.StringFlag{Name: "changelog-config", Required: true, Sources: cli.EnvVars("CHANGELOG_CONFIG"), Usage: "renderer config for CHANGELOG.md"},
			&cli.StringFlag{Name: "commit-body-config", Required: true, Sources: cli.EnvVars("COMMIT_BODY_CONFIG"), Usage: "renderer config for the release bump commit body"},
			&cli.StringFlag{Name: "commit-trailers", Sources: cli.EnvVars("COMMIT_TRAILERS"), Usage: "commit trailers appended after [skip ci]"},
			&cli.StringFlag{Name: "changelog-path", Value: defaultChangelogFile, Sources: cli.EnvVars("CHANGELOG_PATH"), Usage: "output changelog path"},
			&cli.StringFlag{Name: "commit-body-path", Value: defaultCommitBodyFile, Sources: cli.EnvVars("COMMIT_BODY_PATH"), Usage: "output commit body path"},
			&cli.StringFlag{Name: "commit-message-file", Value: defaultCommitMessageFile, Sources: cli.EnvVars("COMMIT_MESSAGE_FILE"), Usage: "output commit message path"},
			&cli.StringFlag{Name: "remote", Value: "origin", Sources: cli.EnvVars("RELEASE_REMOTE"), Usage: "git remote to query"},
			&cli.StringFlag{Name: flagBranch, Value: "main", Usage: "branch containing the release bump (no environment source, like the push)"},
			clitoken.FileFlag("Read token for private repository fetches."),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			token, err := clitoken.Resolve(cmd)
			if err != nil {
				return err
			}

			return appversion.ChangelogRender(ctx, git.New(), &changelog.Renderer{UnsetEnv: secretenv.Names()}, os.Stderr, appversion.ChangelogRenderInput{
				Backend:           cmd.String("backend"),
				Tag:               cmd.String(flagTag),
				RepositoryURL:     cmd.String(flagRepositoryURL),
				Remote:            cmd.String("remote"),
				Branch:            cmd.String(flagBranch),
				ChangelogConfig:   cmd.String("changelog-config"),
				CommitBodyConfig:  cmd.String("commit-body-config"),
				ChangelogPath:     cmd.String("changelog-path"),
				CommitBodyPath:    cmd.String("commit-body-path"),
				CommitMessagePath: cmd.String("commit-message-file"),
				CommitTrailers:    cmd.String("commit-trailers"),
				Token:             token,
			})
		},
	}
}
