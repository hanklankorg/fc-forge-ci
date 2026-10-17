// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli_test

import (
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	internalcli "github.com/diggsweden/forge-ci/internal/cli"
)

// urfave/cli takes the first backticked word of a flag's Usage as the value
// placeholder in --help, so `--plan config parse-artifacts` would replace
// "string". Flag usages quote with double quotes instead.
func TestFlagUsageHasNoBacktick(t *testing.T) {
	t.Parallel()

	var walk func(cmd *cli.Command, path string)

	walk = func(cmd *cli.Command, path string) {
		for _, flag := range cmd.Flags {
			usage, ok := flag.(interface{ GetUsage() string })
			if ok && strings.Contains(usage.GetUsage(), "`") {
				t.Errorf("%s --%s: usage has a backtick, which --help turns into the value placeholder: %s",
					path, flag.Names()[0], usage.GetUsage())
			}
		}

		for _, sub := range cmd.Commands {
			walk(sub, path+" "+sub.Name)
		}
	}

	walk(internalcli.New(internalcli.BuildInfo{}), "forge-ci")
}
