// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
)

// signingKeyCmd wires `forge-ci validate signing-key`. The signer side of
// the trust anchor: before a job signs anything, assert that the private key it
// was handed derives the public key consumers already trust.
//
// Distinct from `validate artifact-signature` and `validate container-
// signature`, which check something already signed. This checks the key itself,
// before the first signature exists.
//
// The derivation runs through the cosign adapter isolated for the key
// reference, so an env:// key reaches a subprocess holding only the runtime
// environment plus that variable and COSIGN_PASSWORD.
func signingKeyCmd() *cli.Command {
	return &cli.Command{
		Name:  "signing-key",
		Usage: "verify a cosign signing key derives the trusted public key, before it is used to sign",
		Description: `EXAMPLE:
   forge-ci validate signing-key --key env://COSIGN_KEY \
     --trusted-public-key keys/cosign.pub --trusted-public-key-digest keys/cosign.pub.sha256`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     flagKey,
				Required: true,
				Sources:  cli.EnvVars("SIGN_KEY"),
				Usage:    "cosign key reference whose public half is derived (e.g. env://COSIGN_KEY, a KMS URI, or a private key file)",
			},
			&cli.StringFlag{
				Name:     "trusted-public-key",
				Required: true,
				Usage:    "path to the public key the signing key must match; which key is trusted is the caller's policy",
			},
			&cli.StringFlag{
				Name:  "trusted-public-key-digest",
				Usage: "optional sidecar recording the trusted public key's SHA-256 (sha256sum format), checked before it is used as the comparison target",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			keyRef := cmd.String(flagKey)

			return appvalidate.VerifySigningKey(ctx, cosign.ForKeyRef(keyRef), os.Stdout, appvalidate.SigningKeyInput{
				KeyRef:               keyRef,
				TrustedPublicKeyPath: cmd.String("trusted-public-key"),
				TrustedDigestPath:    cmd.String("trusted-public-key-digest"),
			})
		},
	}
}
