// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
)

// allowedSignersCmd wires `forge-ci validate allowed-signers`. It is a
// pre-flight format check on the OpenSSH allowed_signers file — catches
// malformed entries (missing principal, unknown key type, invalid base64)
// before they reach CI tag-verification, where the error is less obvious.
func allowedSignersCmd() *cli.Command {
	return &cli.Command{
		Name:  "allowed-signers",
		Usage: "check SSH allowed_signers entries and public keys",
		Description: `Checks each non-comment line of .forge-ci/allowed_signers
for principal/key structure and supported option names:

  principal [options] key-type base64-key

OpenSSH evaluates option values, principal matching and authorization.
Empty files authorize nobody. Reports each malformed entry and exits
non-zero on failure. Useful as a pre-commit hook or CI pre-flight check.

EXAMPLE:
   forge-ci validate allowed-signers
   forge-ci validate allowed-signers --path .forge-ci/allowed_signers`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    flagPath,
				Sources: cli.EnvVars("ALLOWED_SIGNERS_PATH"),
				Usage:   "path to the allowed_signers file (default: .forge-ci/allowed_signers)",
				Value:   ".forge-ci/allowed_signers",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			path := cmd.String(flagPath)

			if err := appvalidate.AllowedSignersFormat(path); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(os.Stdout, "✓ %s passed basic entry/key checks; OpenSSH evaluates option values and authorization\n", path)

			return nil
		},
	}
}
