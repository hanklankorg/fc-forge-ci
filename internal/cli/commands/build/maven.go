// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/maven"
	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/cli/commonflags"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func mavenCmd() *cli.Command {
	return &cli.Command{
		Name:  "maven",
		Usage: "maven build wrappers",
		Commands: []*cli.Command{
			mavenRunCmd(),
			mavenMetadataCmd(),
		},
	}
}

// mavenRunCmd runs the whole Maven release build in one step (the Maven sibling
// of `build go run`); the granular subcommands remain the composable units.
func mavenRunCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdRun,
		Usage: "run the full Maven release build (install, metadata, app/lib lifecycle, SBOM, summary)",
		Description: `Runs the whole Maven build sequence in one step: install modules to the
   local repo, resolve metadata, run the application lifecycle (--build-type app)
   or library lifecycle and optional profile (--build-type lib), generate the
   Build SBOM with the pinned cyclonedx-maven-plugin (unless --no-build-sbom),
   and write the summaries. The project POM/profile owns attached artifacts.

   mvn executes in the current directory, not --working-dir: run this from the
   project root, or cd into the module first (the forge job does, then wraps it
   with checkout + artifact upload). --working-dir only locates the pom.xml for
   metadata.

EXAMPLE:
   cd module && forge-ci build maven run --build-type app --cli-opts "-B -ntp" --sbom-tool-version 2.9.3`,
		Flags: append(dependencyFlags(), []cli.Flag{
			&cli.StringFlag{Name: "settings-path", Sources: cli.EnvVars("MAVEN_SETTINGS_PATH"), Usage: "consumer-owned Maven settings; required with private dependencies; reference DEPENDENCY_TOKEN and DEPENDENCY_USERNAME"},
			&cli.StringFlag{Name: flagWorkingDir, Value: ".", Sources: cli.EnvVars("WORKING_DIRECTORY"), Usage: "directory of the pom.xml to read for metadata; mvn itself runs in the current directory (cd in first)"},
			&cli.StringFlag{Name: "build-type", Sources: cli.EnvVars("BUILD_TYPE"), Usage: "lifecycle: \"app\" (clean package) or \"lib\" (compile/test/package with optional --profile; POM owns attached artifacts)"},
			&cli.StringFlag{Name: flagCLIOpts, Sources: cli.EnvVars("MAVEN_CLI_OPTS"), Usage: "extra args forwarded to mvn (whitespace-separated, e.g. \"-B -ntp\")"},
			&cli.StringFlag{Name: "profile", Sources: cli.EnvVars("MAVEN_PROFILE"), Usage: "Maven profile to activate (library builds)"},
			&cli.BoolFlag{Name: flagSkipTests, Sources: cli.EnvVars("SKIP_TESTS"), Usage: "skip the Maven test phase"},
			&cli.BoolFlag{Name: flagBuildSBOM, Value: true, Sources: cli.EnvVars("ENABLE_BUILD_SBOM"), Usage: "generate the cyclonedx-maven-plugin Build SBOM (default true)"},
			&cli.StringFlag{Name: flagSBOMToolVersion, Sources: cli.EnvVars("CYCLONEDX_MAVEN_VERSION"), Usage: "pinned org.cyclonedx:cyclonedx-maven-plugin version (required when --build-sbom)"},
			&cli.StringFlag{Name: "java-version", Sources: cli.EnvVars("JAVA_VERSION"), Usage: "JDK major version, reported in the build summary"},
			&cli.StringFlag{Name: "expect-version", Usage: "fail unless the project already declares this version (a release build; leading v allowed)"},
		}...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			settings := cmd.String("settings-path")
			if cmd.Bool("private-dependencies") && settings == "" {
				return fmt.Errorf("private Maven dependencies require --settings-path with the project's repository authentication; existing mirrors/settings are never overwritten: %w", errs.ErrInvalidConfig)
			}

			var options []string

			if settings != "" {
				info, err := os.Stat(settings)
				if err != nil {
					return fmt.Errorf("dependency settings file: %w: %w", err, errs.ErrMissingInput)
				}

				if !info.Mode().IsRegular() {
					return fmt.Errorf("dependency settings must be a regular file: %w", errs.ErrInvalidConfig)
				}

				options = []string{"--settings", settings}
			}

			environment, err := resolveDependencyEnvironment(cmd)
			if err != nil {
				return err
			}

			if cmd.Bool("validate-dependencies-only") {
				return nil
			}

			return dependencyDiagnostics(environment.token, func(out, stderr io.Writer) error {
				return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
					return appbuild.MavenReleaseBuild(ctx, d.SummarySink, &maven.Adapter{Env: environment.authenticated, Options: options}, out, stderr, appbuild.MavenReleaseBuildInput{
						ReleaseBuildOptions: appbuild.ReleaseBuildOptions{
							Dir:             cmd.String(flagWorkingDir),
							SkipTests:       cmd.Bool(flagSkipTests),
							EnableBuildSBOM: cmd.Bool(flagBuildSBOM),
						},
						BuildType:       cmd.String("build-type"),
						CLIOpts:         strings.Fields(cmd.String(flagCLIOpts)),
						Profile:         cmd.String("profile"),
						SBOMToolVersion: cmd.String(flagSBOMToolVersion),
						JavaVersion:     cmd.String("java-version"),
						ExpectVersion:   cmd.String("expect-version"),
					})
				})
			})
		},
	}
}

func mavenMetadataCmd() *cli.Command {
	return &cli.Command{
		Name:  "metadata", //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
		Usage: "parse pom.xml for {version,groupId,artifactId} and emit CI outputs (falls back to `mvn help:evaluate` only for ${property} references)",
		Description: `EXAMPLE:
   forge-ci build maven metadata --working-dir .`,
		Flags: append(dependencyAccessFlags(), []cli.Flag{
			commonflags.WorkingDir("directory containing the pom.xml to parse"),
			&cli.StringFlag{Name: "settings-path", Sources: cli.EnvVars("MAVEN_SETTINGS_PATH", "SETTINGS_PATH"), Usage: "consumer-owned settings used when metadata needs Maven evaluation"},
			&cli.StringFlag{Name: "profile", Sources: cli.EnvVars("MAVEN_PROFILE"), Usage: "profile used for resolved metadata"},
		}...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			environment, err := resolveDependencyEnvironment(cmd)
			if err != nil {
				return err
			}

			var options []string
			if settings := cmd.String("settings-path"); settings != "" {
				options = append(options, "--settings", settings)
			}

			if profile := cmd.String("profile"); profile != "" {
				options = append(options, "-P"+profile)
			}

			return dependencyDiagnostics(environment.token, func(out, _ io.Writer) error {
				return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
					return appbuild.MavenMetadata(ctx, d.OutputSink, &maven.Adapter{Env: environment.authenticated, Options: options}, out, appbuild.MavenMetadataInput{Dir: cmd.String("working-dir")})
				})
			})
		},
	}
}
