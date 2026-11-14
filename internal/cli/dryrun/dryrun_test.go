// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package dryrun_test

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli/dryrun"
)

// A deleting command previews unless told otherwise, and its help line says
// so: the CLI library prints no default for a boolean that is true.
func TestDeletionFlagPreviewsByDefaultAndSaysSo(t *testing.T) {
	t.Parallel()

	for args, want := range map[string]bool{"": true, "--dry-run=false": false} {
		var got bool

		cmd := &cli.Command{
			Name:  "prune",
			Flags: []cli.Flag{dryrun.DeletionFlag("FORGE_CI_TEST_UNSET_DRY_RUN")},
			Action: func(_ context.Context, cmd *cli.Command) error {
				got = dryrun.Enabled(cmd)

				return nil
			},
		}

		argv := []string{"prune"}
		if args != "" {
			argv = append(argv, args)
		}

		if err := cmd.Run(context.Background(), argv); err != nil {
			t.Fatal(err)
		}

		if got != want {
			t.Errorf("args %q: dry run = %v, want %v", args, got, want)
		}
	}

	usage := dryrun.DeletionFlag("X").(*cli.BoolFlag).Usage
	if !strings.Contains(usage, "default: true") || !strings.Contains(usage, "--dry-run=false") {
		t.Errorf("usage %q does not name the default and how to delete", usage)
	}
}
