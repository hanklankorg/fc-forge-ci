// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"context"
	"fmt"
	enginecli "github.com/diggsweden/forge-ci/internal/cli"
	"os"
	"slices"
	"testing"
)

// TestOwnerCLI runs the real command tree only in explicitly marked child
// processes. Shell-adapter tests exercise Go owners without rebuilding a CLI.
func TestOwnerCLI(t *testing.T) {
	if os.Getenv("OWNER_CLI_HELPER") != "1" {
		return
	}

	index := slices.Index(os.Args, "--")
	if index < 0 {
		os.Exit(99)
	}

	err := enginecli.New(enginecli.BuildInfo{}).Run(context.Background(), append([]string{"forge-ci"}, os.Args[index+1:]...))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}

	os.Exit(0)
}

const ownerCLI = `rc_engine() { OWNER_CLI_HELPER=1 "$OWNER_CLI_BINARY" -test.run='^TestOwnerCLI$' -- "$@"; }; `
