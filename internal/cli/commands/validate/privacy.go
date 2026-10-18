// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"context"
	"fmt"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/urfave/cli/v3"
)

func signingPrivacyCmd() *cli.Command {
	return &cli.Command{
		Name:  "signing-privacy",
		Usage: "refuse implicit public transparency for private or unknown repositories before signing",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "repository-visibility", Sources: cli.EnvVars("REPOSITORY_VISIBILITY"), Usage: "public, private, internal or unknown; omission is unknown"},
			&cli.StringFlag{Name: "method", Sources: cli.EnvVars("SIGN_METHOD"), Usage: "signing method: sigstore, kms or gpg"},
			&cli.StringFlag{Name: "transparency", Sources: cli.EnvVars("TRANSPARENCY_LOG"), Usage: "explicit public or none; omission defaults to public but is not consent"},
			&cli.BoolFlag{Name: "enabled", Value: true, Sources: cli.EnvVars("SIGN_ENABLED"), Usage: "whether this phase signs or attests"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			raw := cmd.String("transparency")
			mode := release.DefaultTransparency

			var err error
			if raw != "" {
				mode, err = release.ParseTransparency(raw)
				if err != nil {
					return err
				}
			}

			if !cmd.Bool("enabled") {
				return release.ValidateTransparencyConsent(cmd.String("repository-visibility"), mode, raw != "", false)
			}

			method, err := release.ParseSignMethod(cmd.String("method"))
			if err != nil {
				return err
			}

			if method == release.SignMethodGPG {
				mode = ""
			}

			if method == release.SignMethodSigstore && mode == release.TransparencyNone {
				return fmt.Errorf("keyless signing requires public transparency; use key-based signing to avoid public disclosure: %w", errs.ErrInvalidConfig)
			}

			return release.ValidateTransparencyConsent(cmd.String("repository-visibility"), mode, raw != "", true)
		},
	}
}
