// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cliio"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func runtimePublicationPromoteCmd() *cli.Command {
	return &cli.Command{
		Name: "promote", Usage: "verify native signatures and current main before promoting the exact runtime index to its source tag",
		Description: "Run from the downloaded evidence directory. Requires a provider with authenticated branch-head resolution (currently Forgejo). Writes published-ref.txt only after the destination serves the signed digest. No signing key is accepted.",
		Flags:       append(append(append(signerImageSourceFlags(), multiarchImageParamFlags()...), baseImagesPublicKeyFlags()...), multiarchImageArchsFlag()),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if !domaincontainer.ValidSHA256Hex(cmd.String("cosign-public-key-sha256")) {
				return fmt.Errorf("runtime promotion requires the reviewed Cosign public-key digest: %w", errs.ErrValidation)
			}

			if err := validateReleaseImagePublicKey(cmd.String("cosign-public-key-path"), cmd.String("cosign-public-key-sha256")); err != nil {
				return err
			}

			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				source, err := dep.RequireBranchHeadResolver()
				if err != nil {
					return err
				}

				return withReleaseImageDockerConfig(cmd.String(flagAuthFile), func() error {
					ref, err := appcontainer.PromoteRuntimePublication(ctx,
						ociregistry.WithAuthFile(cmd.String(flagAuthFile)), cosign.NewIsolated("DOCKER_CONFIG"), source, os.Stderr,
						appcontainer.RuntimePublicationPromoteInput{Manifest: runtimeManifestInput(cmd), PublicKey: cmd.String("cosign-public-key-path")})
					if err != nil {
						return err
					}

					if err := cliio.WriteFile("published-ref.txt", []byte(ref+"\n"), 0o600); err != nil {
						return err
					}

					_, err = fmt.Fprintln(os.Stdout, ref)

					return err
				})
			})
		},
	}
}
