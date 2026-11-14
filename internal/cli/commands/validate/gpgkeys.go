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

// gpgKeysCmd wires `forge-ci validate gpg-keys`. Structural format check
// on the armored GPG public-key bundle — catches truncated or
// marker-less files before they reach CI tag-verification.
func gpgKeysCmd() *cli.Command {
	return &cli.Command{
		Name:  "gpg-keys",
		Usage: "validate the armored GPG public-key bundle format",
		Description: `Checks .forge-ci/allowed_gpg_keys.asc for well-formed
public keys: at least one complete PGP PUBLIC KEY BLOCK, with
parseable public keys in every block. Uses the release verifier's
key-bundle parser; this does not verify a release signature.

Exits non-zero with errors if the file is malformed. Useful as
a pre-commit hook or CI pre-flight gate.

EXAMPLE:
   forge-ci validate gpg-keys
   forge-ci validate gpg-keys --path .forge-ci/allowed_gpg_keys.asc`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    flagPath,
				Sources: cli.EnvVars("ALLOWED_GPG_KEYS_PATH"),
				Usage:   "path to the armored GPG public-key bundle (default: .forge-ci/allowed_gpg_keys.asc)",
				Value:   ".forge-ci/allowed_gpg_keys.asc",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			path := cmd.String(flagPath)

			if err := appvalidate.GPGKeysFormat(path); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(os.Stdout, "✓ %s contains parseable public keys in complete armor blocks\n", path)

			return nil
		},
	}
}
