// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/maven"
	"github.com/diggsweden/forge-ci/internal/adapters/mavenregistry"
	"github.com/diggsweden/forge-ci/internal/adapters/npm"
	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/cli/commonflags"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func forgePackagesCmd() *cli.Command {
	return &cli.Command{
		Name:  "forge-packages",
		Usage: "publish to the forge-native package registry (GitHub Packages / GitLab Package Registry / Forgejo)",
		Commands: []*cli.Command{
			forgePackagesDeployCmd(),
		},
	}
}

func forgePackagesDeployCmd() *cli.Command {
	return &cli.Command{
		Name:  commandDeploy,
		Usage: "publish to the detected forge's package registry (Maven, npm, or exported Gradle publications)",
		Description: `Resolves the forge-native registry for the active forge + ecosystem (URL +
   auth), writes a credentialed settings.xml/.npmrc to a 0600 temp file (the
   token never reaches argv), and runs the deploy — one binary-owned sequence
   replacing the per-forge inline mvn/npm. The forge is auto-detected (override
   with --provider). Runs in the current directory; cd into the project first.

   --project-type maven: mvn deploy. --project-type npm: npm publish the *.tgz.

EXAMPLE:
   cd module && forge-ci publish forge-packages deploy --project-type maven`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: flagSnapshot, Sources: cli.EnvVars("MAVEN_SNAPSHOT"), Usage: "publish only Maven-format snapshots; rejected for npm"},
			&cli.StringFlag{Name: "project-type", Value: "maven", Sources: cli.EnvVars("PROJECT_TYPE"), Usage: "ecosystem: maven, npm, or gradle (exported Maven publications)"},
			&cli.StringFlag{Name: flagVersion, Sources: cli.EnvVars("VERSION"), Usage: "expected version for exported Gradle publications"},
			&cli.StringFlag{Name: "cli-opts", Sources: cli.EnvVars("MAVEN_CLI_OPTS"), Usage: "extra args forwarded to mvn (maven only; whitespace-separated)"},
			commonflags.WorkingDir("directory holding the packed *.tgz (npm only)"),
			&cli.StringFlag{Name: flagNPMTag, Sources: cli.EnvVars("NPM_TAG"), Usage: "required for npm: release-{version} or release-<packed-version>; latest, next and movable channel tags are forbidden"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error { //nolint:varnamelen // idiomatic short name for the deps handle.
				switch projectType := cmd.String("project-type"); projectType {
				case "npm":
					if cmd.Bool("snapshot") {
						return fmt.Errorf("npm uses explicit version-specific tags, not Maven snapshot mode: %w", errs.ErrUsage)
					}

					resolver, err := d.RequireForgeNPMRegistryResolver()
					if err != nil {
						return err
					}

					return apppublish.ForgePackagesNPMPublish(ctx, npm.New(), resolver, os.Stderr, os.Stderr, apppublish.ForgePackagesNPMPublishInput{
						WorkingDir: cmd.String("working-dir"),
						Tag:        cmd.String("npm-tag"),
					})
				case "maven", "":
					if cmd.Bool("snapshot") {
						if err := apppublish.ValidateMavenDeployment(ctx, mavenModelAdapter(), os.Stderr, apppublish.MavenCentralDeployInput{CLIOpts: strings.Fields(cmd.String("cli-opts"))}, true, ""); err != nil {
							return err
						}
					}

					resolver, err := d.RequireForgeMavenRegistryResolver()
					if err != nil {
						return err
					}

					return apppublish.ForgePackagesDeploy(ctx, maven.New(), resolver, os.Stderr, os.Stderr, apppublish.ForgePackagesDeployInput{
						CLIOpts: strings.Fields(cmd.String("cli-opts")),
					})
				case "gradle", "gradle-android":
					return deployExportedMaven(ctx, cmd, d)
				default:
					return fmt.Errorf("--project-type must be maven, npm or gradle, got %q: %w", projectType, errs.ErrUsage)
				}
			})
		},
	}
}

func deployExportedMaven(ctx context.Context, cmd *cli.Command, dependencies *deps.Deps) error {
	resolver, err := dependencies.RequireForgeMavenRegistryResolver()
	if err != nil {
		return err
	}

	if cmd.Bool(flagSnapshot) {
		registry, resolveErr := resolver.ResolveForgeMavenRegistry()
		if resolveErr != nil {
			return resolveErr
		}

		return apppublish.UploadMavenSnapshots(ctx, snapshotMavenAdapter(), registry, os.Stderr, cmd.String("working-dir"))
	}

	return apppublish.UploadMavenPublication(ctx, mavenregistry.Client{}, resolver, os.Stderr, cmd.String("working-dir"), strings.TrimPrefix(cmd.String(flagVersion), "v"))
}
