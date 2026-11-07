// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestLedgerSignCommandsExposeOptInEvidenceRoot(t *testing.T) {
	for _, command := range []*cli.Command{ledgerSignCmd(), baseImagesSignCmd(), releaseImagesSignCmd()} {
		t.Run(command.Usage, func(t *testing.T) {
			// Binding only: do not acquire auth, resolve a registry or invoke tools.
			for _, root := range []string{"", "private/verified-workspace"} {
				var got string

				command.Action = func(_ context.Context, parsed *cli.Command) error {
					got = parsed.String("evidence-root")

					return nil
				}

				args := []string{command.Name, "--ledger", "dist/images.json"}
				if root != "" {
					args = append(args, "--evidence-root", root)
				}

				require.NoError(t, command.Run(t.Context(), args))
				require.Equal(t, root, got)
			}
		})
	}
}
