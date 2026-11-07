// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Command gen-runby writes internal/cli/runby.json: for each forge-ci
// command, the reusable workflows, actions and GitLab templates of this
// repository that invoke it, for the RUN BY section of --help.
//
// Usage:
//
//	just gen-runby
//
// Re-run after a workflow starts or stops calling a command;
// TestRunByTableInSync fails until then.
package main

import (
	"fmt"
	"os"

	"github.com/diggsweden/forge-ci/internal/cli"
)

func main() {
	body, err := cli.GenerateRunBy(".")
	if err == nil {
		_, err = fmt.Fprint(os.Stdout, body)
	}

	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)

		os.Exit(1)
	}
}
