// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"errors"
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
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func forgePackagesCmd() *cli.Command {
	return &cli.Command{
		Name:  "forge-packages",
		Usage: "publish to the forge-native package registry (GitHub Packages / GitLab Package Registry / Forgejo)",
		Commands: []*cli.Command{
			forgePackagesDeployCmd(),
			forgePackagesDistroPackagesCmd(),
			forgePackagesNPMTarballsCmd(),
		},
	}
}

const forgeMavenProject = "maven"

const (
	flagDistDir    = "dist-dir"
	flagProjectDir = "project-dir"
)

func forgePackagesNPMTarballsCmd() *cli.Command {
	return &cli.Command{
		Name:  "npm-tarballs",
		Usage: "name each npm project's packed tarball in a dist directory, refusing any other",
		Description: `Prints, one a line and in the order of --project-dir, the name of the
   *.tgz at the root of --dist-dir whose packed package.json carries the name
   and version of that project's package.json in the current directory. It
   fails when a project has no tarball, or the directory holds a *.tgz of no
   project: a release publishes exactly the packages its plan names.

EXAMPLE:
   forge-ci publish forge-packages npm-tarballs --dist-dir dist --project-dir frontend`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagDistDir, Required: true, Usage: "directory holding the packed tarballs at its root"},
			&cli.StringSliceFlag{Name: flagProjectDir, Required: true, Usage: "npm project directory, relative to the current directory; repeat for each"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			names, err := apppublish.NPMTarballs(".", cmd.String(flagDistDir), cmd.StringSlice(flagProjectDir))
			if err != nil {
				return err
			}

			for _, name := range names {
				if _, err := fmt.Fprintln(os.Stdout, name); err != nil {
					return err
				}
			}

			return nil
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
			&cli.StringFlag{Name: "project-type", Value: forgeMavenProject, Sources: cli.EnvVars("PROJECT_TYPE"), Usage: "ecosystem: maven, npm, or gradle (exported Maven publications)"},
			&cli.StringFlag{Name: flagVersion, Sources: cli.EnvVars("VERSION"), Usage: "expected version for prebuilt Maven or exported Gradle publications"},
			&cli.BoolFlag{Name: "from-build", Sources: cli.EnvVars("MAVEN_FROM_BUILD"), Usage: "publish Maven library POM/main/sources/Javadoc build bytes without rerunning the project lifecycle"},
			&cli.StringFlag{Name: "cli-opts", Sources: cli.EnvVars("MAVEN_CLI_OPTS"), Usage: "extra args forwarded to mvn (maven only; whitespace-separated)"},
			&cli.StringFlag{Name: "profile", Sources: cli.EnvVars("MAVEN_PROFILE"), Usage: "optional project-owned Maven publication profile; also used for snapshot version preflight"},
			commonflags.WorkingDir("packed npm tarball, Maven build handoff or exported Gradle publication directory"),
			&cli.StringFlag{Name: flagNPMTag, Sources: cli.EnvVars("NPM_TAG"), Usage: "required for npm: release-{version}, release.{version}, release-<packed-version> or release.<packed-version>; latest, next and movable channel tags are forbidden"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return deployForgePackages(ctx, cmd, d)
			})
		},
	}
}

func deployForgePackages(ctx context.Context, cmd *cli.Command, dependencies *deps.Deps) error {
	if cmd.Bool("from-build") && cmd.String("project-type") != forgeMavenProject && cmd.String("project-type") != "" {
		return fmt.Errorf("--from-build selects Maven library publication: %w", errs.ErrUsage)
	}

	switch projectType := cmd.String("project-type"); projectType {
	case "npm":
		return deployForgeNPM(ctx, cmd, dependencies)
	case forgeMavenProject, "":
		return deployForgeMaven(ctx, cmd, dependencies)
	case "gradle", "gradle-android":
		return deployExportedMaven(ctx, cmd, dependencies)
	default:
		return fmt.Errorf("--project-type must be maven, npm or gradle, got %q: %w", projectType, errs.ErrUsage)
	}
}

func deployForgeNPM(ctx context.Context, cmd *cli.Command, dependencies *deps.Deps) error {
	if cmd.Bool("snapshot") {
		return fmt.Errorf("npm uses explicit version-specific tags, not Maven snapshot mode: %w", errs.ErrUsage)
	}

	resolver, err := dependencies.RequireForgeNPMRegistryResolver()
	if err != nil {
		return err
	}

	if verifier, ok := resolver.(provider.ForgeProjectVerifier); ok {
		if err := verifier.VerifyForgeProject(ctx); err != nil {
			return err
		}
	}

	ops := &npm.Adapter{Env: apppublish.NPMPublishEnvironment(os.Environ())}

	return apppublish.ForgePackagesNPMPublish(ctx, ops, resolver, os.Stderr, os.Stderr, apppublish.ForgePackagesNPMPublishInput{
		WorkingDir: cmd.String("working-dir"),
		Tag:        cmd.String("npm-tag"),
	})
}

func deployForgeMaven(ctx context.Context, cmd *cli.Command, dependencies *deps.Deps) error {
	if cmd.Bool("from-build") {
		return deployMavenBuild(ctx, cmd, dependencies)
	}

	if cmd.Bool("snapshot") {
		if err := apppublish.ValidateMavenDeployment(ctx, mavenModelAdapter(), os.Stderr, apppublish.MavenCentralDeployInput{CLIOpts: strings.Fields(cmd.String("cli-opts")), Profile: cmd.String("profile")}, true, ""); err != nil {
			return err
		}
	}

	resolver, err := dependencies.RequireForgeMavenRegistryResolver()
	if err != nil {
		return err
	}

	return apppublish.ForgePackagesDeploy(ctx, maven.New(), resolver, os.Stderr, os.Stderr, apppublish.ForgePackagesDeployInput{
		CLIOpts: strings.Fields(cmd.String("cli-opts")),
		Profile: cmd.String("profile"),
	})
}

func deployMavenBuild(ctx context.Context, cmd *cli.Command, dependencies *deps.Deps) (result error) {
	directory, err := os.MkdirTemp("", "forge-ci-maven-build-publication-*")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(directory)) }()

	version := strings.TrimPrefix(cmd.String(flagVersion), "v")
	if err = apppublish.StageMavenBuild(ctx, cmd.String("working-dir"), directory, version, cmd.Bool(flagSnapshot)); err != nil {
		return err
	}

	resolver, err := dependencies.RequireForgeMavenRegistryResolver()
	if err != nil {
		return err
	}

	if cmd.Bool(flagSnapshot) {
		registry, err := resolver.ResolveForgeMavenRegistry()
		if err != nil {
			return err
		}

		return apppublish.UploadMavenSnapshots(ctx, snapshotMavenAdapter(), registry, os.Stderr, directory)
	}

	return apppublish.UploadMavenPublication(ctx, mavenregistry.Client{}, resolver, os.Stderr, directory, version)
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
