// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/clitoken"
)

// verifyRequestCmd is the Go port of forgejo-ci's verify-release-request.sh:
// verify that a stable release-request/vX.Y.Z tag on origin authorises the
// final vX.Y.Z tag and is SSH-signed by an allowed signer. With
// --expected-commit it also binds the request and the checkout to the commit
// the caller took from its trusted event context, which is what a caller needs
// to re-run it immediately before signing.
func verifyRequestCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate-request",
		Usage: "validate an SSH-signed release-request tag authorizes creating a final release tag",
		Description: `EXAMPLE:
   forge-ci release validate-request --release-request release-request/v1.2.3 --tag v1.2.3 --allowed-signers-file .forgejo/release-request.allowed_signers`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "release-request", Required: true, Sources: cli.EnvVars("RELEASE_REQUEST"), Usage: "release request tag (release-request/vMAJOR.MINOR.PATCH)"},
			&cli.StringFlag{Name: flagTag, Required: true, Sources: cienv.Tag(), Usage: "final stable release tag (vMAJOR.MINOR.PATCH)"},
			&cli.StringFlag{Name: "allowed-signers-file", Sources: cli.EnvVars("RELEASE_REQUEST_ALLOWED_SIGNERS"), Usage: "OpenSSH allowed_signers file for release-request SSH signatures (required unless --skip-signature)"},
			&cli.StringFlag{Name: "remote", Value: "origin", Usage: "git remote queried for request/final tags"},
			&cli.StringFlag{Name: "expected-commit", Sources: cli.EnvVars("RELEASE_REQUEST_SHA"), Usage: "commit the request tag and the checkout must both be at, bound by the caller from the trusted event context"},
			clitoken.FileFlag("Read token for private repository fetches."),
			&cli.BoolFlag{Name: "skip-signature", Usage: "adopt-time escape hatch: run every check except the SSH signature; a production release must not set it"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			token, err := clitoken.Resolve(cmd)
			if err != nil {
				return err
			}

			return apprelease.VerifyReleaseRequest(ctx, git.New(), os.Stdout, apprelease.VerifyReleaseRequestInput{
				ReleaseRequest:     cmd.String("release-request"),
				ReleaseTag:         cmd.String(flagTag),
				AllowedSignersPath: cmd.String("allowed-signers-file"),
				Remote:             cmd.String("remote"),
				ExpectedCommit:     cmd.String("expected-commit"),
				SkipSignature:      cmd.Bool("skip-signature"),
				Token:              token,
			})
		},
	}
}
