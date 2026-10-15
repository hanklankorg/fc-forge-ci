// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/github"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

// publishSelfRuntimeCLICmd is hidden because it is an implementation detail of
// this repository's self-runtime workflow, not a consumer release primitive.
func publishSelfRuntimeCLICmd() *cli.Command {
	return &cli.Command{
		Name:   "publish-self-runtime-cli",
		Usage:  "replace the allowed repository's mutable " + domainrelease.SelfRuntimeChannelTag + " CLI release at an exact commit",
		Hidden: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagRepository, Required: true, Sources: cienv.Repository(), Usage: "must equal --allowed-repository"},
			&cli.StringFlag{Name: "source-ref", Required: true, Usage: "refs/heads/<allowed-branch>, which must still resolve to --commit"},
			&cli.StringFlag{Name: "commit", Required: true, Sources: cienv.Commit(), Usage: "full commit SHA the recreated tag must resolve to"},
			&cli.StringFlag{Name: "assets-dir", Required: true, Usage: "directory containing the exact signed build-cli GoReleaser asset set"},
			&cli.StringFlag{Name: "allowed-repository", Sources: cli.EnvVars("FORGE_CI_REPOSITORY"), Usage: "repository allowed to publish the channel (default " + domainrelease.SelfRuntimeRepository + ")"},
			&cli.StringFlag{Name: "allowed-branch", Sources: cli.EnvVars("FORGE_CI_PRERELEASE_BRANCH"), Usage: "branch allowed to publish the channel (default " + domainrelease.SelfRuntimeBranch + ")"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return apprelease.PublishSelfRuntimeCLI(ctx, github.New(), os.Stderr, apprelease.PublishSelfRuntimeCLIInput{
				Policy: domainrelease.SelfRuntimePolicy{
					Repository: cmd.String("allowed-repository"),
					Branch:     cmd.String("allowed-branch"),
				},
				Repository: cmd.String(flagRepository),
				SourceRef:  cmd.String("source-ref"),
				TargetSHA:  cmd.String("commit"),
				AssetsDir:  cmd.String("assets-dir"),
			})
		},
	}
}
