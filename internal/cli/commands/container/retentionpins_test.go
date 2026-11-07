// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestRetentionInventoryAllowsPinOnlyRulesAndRefusesMixedSelectors(t *testing.T) {
	raw := fmt.Sprintf(`{"include":[{"repository":"ghcr.io/acme/runtime","digest":"sha256:%s"}]}`, strings.Repeat("a", 64))
	for _, selection := range [][]string{
		nil, {"--repository", "ghcr.io/acme/runtime"}, {"--repository-prefix", "ghcr.io/acme/"},
		{"--keep-digest", "sha256:" + strings.Repeat("b", 64)},
	} {
		command := packageRetentionCmd()
		command.Action = func(_ context.Context, cmd *cli.Command) error {
			_, err := retentionRuleFromCmd(cmd)

			return err
		}

		args := make([]string, 0, 6+len(selection))
		args = append(args, "package-retention", "--packages-json", raw, "--keep-tags=", "--recent-tags=", "--recent-count=0")
		args = append(args, selection...)

		err := command.Run(t.Context(), args)
		if (err == nil) != (len(selection) == 0) {
			t.Fatalf("inventory selector admission %v: %v", selection, err)
		}
	}
}

func TestRetentionDigestRequiresExactlyOnePackage(t *testing.T) {
	for _, selection := range [][]string{
		nil,
		{"--repository-prefix", "ghcr.io/acme/runtime"},
		{"--repository", "ghcr.io/acme/one", "--repository", "ghcr.io/acme/two"},
		{"--repository", "ghcr.io/acme/one"},
	} {
		command := packageRetentionCmd()
		command.Action = func(_ context.Context, cmd *cli.Command) error {
			_, err := retentionRuleFromCmd(cmd)

			return err
		}

		err := command.Run(t.Context(), append([]string{"package-retention", "--keep-digest", "sha256:" + strings.Repeat("a", 64)}, selection...))
		if (err == nil) != (len(selection) == 2 && selection[0] == "--repository") {
			t.Fatalf("digest/package binding: %v", err)
		}
	}
}
