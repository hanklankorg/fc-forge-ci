// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
)

// flagRoot is the shared "root" flag name across the validate subcommands.
const flagRoot = "root"

// workflowGroup wires `forge-ci validate workflow <verb>` — every
// subcommand here scans the repository's reusable workflow YAML for a
// structural rule (literal-default inputs, removed v3 contracts).
func workflowGroup() *cli.Command {
	return &cli.Command{
		Name:  flagWorkflow,
		Usage: "scan reusable workflow YAML for structural rules",
		Commands: []*cli.Command{
			workflowInputDefaultsCmd(),
		},
	}
}

func workflowInputDefaultsCmd() *cli.Command {
	return &cli.Command{
		Name:  "input-defaults",
		Usage: "verify reusable workflow_call input defaults are literal values, not expressions",
		Description: `EXAMPLE:
   forge-ci validate workflow input-defaults --root .`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagRoot, Value: ".", Usage: "repository root containing .github/workflows"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return appvalidate.WorkflowInputDefaults(os.Stderr, deps.Annotator(cmd), appvalidate.WorkflowInputDefaultsInput{
				Root: cmd.String(flagRoot),
			})
		},
	}
}
