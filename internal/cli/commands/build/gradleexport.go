// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"context"
	"io"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/gradle"
	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/cli/commonflags"
)

func gradleExportCmd() *cli.Command {
	return &cli.Command{
		Name:  "export-publications",
		Usage: "export immutable JVM Maven publications without signing or publishing credentials",
		Flags: append(dependencyAccessFlags(), []cli.Flag{
			commonflags.WorkingDir("prepared Gradle project root"),
			&cli.StringFlag{Name: "project", Usage: "exact Gradle project path to export (e.g. :lib); omitted exports every project"},
			&cli.StringFlag{Name: "output", Required: true, Usage: "fresh output directory for the local Maven-layout repository"},
			&cli.StringFlag{Name: "version", Sources: cli.EnvVars("VERSION"), Usage: "required release version (leading v allowed); optional expected version in snapshot mode"},
			&cli.BoolFlag{Name: "snapshot", Usage: "export only snapshot publications using their resolved versions; no signed release request required"},
			&cli.BoolFlag{Name: "central", Usage: "also require Central metadata, sources and javadoc before exporting"},
			&cli.BoolFlag{Name: "validate-only", Usage: "check static publication inputs before building; no tools, files or credentials are changed"},
		}...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Bool("validate-only") {
				return apppublish.ValidateGradleExport(apppublish.GradleExportInput{Directory: cmd.String("working-dir"), Output: cmd.String("output"), Project: cmd.String("project"), Version: strings.TrimPrefix(cmd.String("version"), "v"), Central: cmd.Bool("central"), Snapshot: cmd.Bool("snapshot")})
			}

			environment, err := resolveDependencyEnvironment(cmd)
			if err != nil {
				return err
			}

			return dependencyDiagnostics(environment.token, func(out, stderr io.Writer) error {
				return apppublish.ExportGradlePublication(ctx, &gradle.Adapter{Env: environment.authenticated}, out, stderr, apppublish.GradleExportInput{
					Directory: cmd.String("working-dir"), Output: cmd.String("output"), Project: cmd.String("project"), Version: strings.TrimPrefix(cmd.String("version"), "v"), Central: cmd.Bool("central"), Snapshot: cmd.Bool("snapshot"),
				})
			})
		},
	}
}
