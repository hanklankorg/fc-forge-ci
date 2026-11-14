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

// jvmReproducibilityCmd exposes the JVM-side counterpart of `validate
// cargo`: fails when a planned Maven/Gradle artifact lacks reproducible-build settings.
func jvmReproducibilityCmd() *cli.Command {
	return &cli.Command{
		Name:  "jvm-reproducibility",
		Usage: "fail when Maven/Gradle artifacts lack reproducible-build settings (outputTimestamp / archive-task config)",
		Description: `CONFIG_PLAN_JSON is produced by ` + "`config parse-artifacts`" + `.

EXAMPLE:
   CONFIG_PLAN_JSON="$(forge-ci config parse-artifacts)" forge-ci validate jvm-reproducibility`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config-plan-json",
				Sources: cli.EnvVars("CONFIG_PLAN_JSON"),
				Usage:   "typed config-plan JSON (output of 'config parse-artifacts')",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			annot := deps.Annotator(cmd)

			return appvalidate.JVMReproducibility(ctx, os.Stderr, annot, appvalidate.JVMReproducibilityInput{
				ConfigPlanJSON: cmd.String("config-plan-json"),
			})
		},
	}
}
