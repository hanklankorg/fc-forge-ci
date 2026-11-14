// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"context"
	"io"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/gradle"
	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
)

func gradleCmd() *cli.Command {
	return &cli.Command{
		Name:  "gradle",
		Usage: "gradle (JVM) build wrappers",
		Commands: []*cli.Command{
			gradleRunCmd(),
			gradleExportCmd(),
		},
	}
}

// gradleRunCmd runs the whole Gradle release build in one step (the Gradle
// sibling of `build go run`).
func gradleRunCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdRun,
		Usage: "run the full Gradle release build (gradlew chmod, metadata, tasks, SBOM, summary)",
		Description: `Runs the whole Gradle build sequence in one step: make ./gradlew
   executable, resolve metadata, invoke mandatory test tasks under administrator
   policy, then run the requested packaging tasks without repeating those tests.
   Generate the Build SBOM with the pinned cyclonedx-gradle-plugin when enabled
   and write the summaries. Consumer --skip-tests=true is refused.

   Test, packaging and SBOM invocations execute in --working-dir, which also
   selects gradle.properties. A successful invocation can include Gradle cached,
   up-to-date or NO-SOURCE outcomes; the summary reports test tasks invoked.

EXAMPLE:
   cd app && forge-ci build gradle run --tasks assemble --sbom-tool-version 3.2.1`,
		Flags: append(dependencyFlags(), []cli.Flag{
			&cli.StringFlag{Name: flagWorkingDir, Value: ".", Sources: cli.EnvVars("WORKING_DIRECTORY"), Usage: "project directory for metadata, mandatory tests, packaging and SBOM"},
			&cli.StringFlag{Name: flagTasks, Required: true, Sources: cli.EnvVars("GRADLE_TASKS"), Usage: usageGradleTasks},
			&cli.BoolFlag{Name: flagSkipTests, Sources: cli.EnvVars("SKIP_TESTS"), Usage: usageSkipTestTask},
			&cli.BoolFlag{Name: flagBuildSBOM, Value: true, Sources: cli.EnvVars("ENABLE_BUILD_SBOM"), Usage: "generate the cyclonedx-gradle-plugin Build SBOM (default true)"},
			&cli.StringFlag{Name: flagSBOMToolVersion, Sources: cli.EnvVars("CYCLONEDX_GRADLE_VERSION"), Usage: "pinned cyclonedx-gradle-plugin version (required when --build-sbom)"},
			&cli.StringFlag{Name: "java-version", Sources: cli.EnvVars("JAVA_VERSION"), Usage: "JDK major version, reported in the build summary"},
		}...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			environment, err := resolveDependencyEnvironment(cmd)
			if err != nil {
				return err
			}

			if cmd.Bool("validate-dependencies-only") {
				return nil
			}

			return dependencyDiagnostics(environment.token, func(out, stderr io.Writer) error {
				return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
					return appbuild.GradleReleaseBuild(ctx, d.SummarySink, &gradle.Adapter{Env: environment.authenticated, NoDaemon: cmd.Bool("private-dependencies")}, out, stderr, appbuild.GradleReleaseBuildInput{
						Dir:             cmd.String(flagWorkingDir),
						SkipTests:       cmd.Bool(flagSkipTests),
						EnableBuildSBOM: cmd.Bool(flagBuildSBOM),
						Tasks:           cmd.String(flagTasks),
						SBOMToolVersion: cmd.String(flagSBOMToolVersion),
						JavaVersion:     cmd.String("java-version"),
					})
				})
			})
		},
	}
}
