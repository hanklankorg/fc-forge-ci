// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/npm"
	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/cli/commonflags"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/secret"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
)

func npmCmd() *cli.Command {
	return &cli.Command{
		Name:  "npm",
		Usage: "npm publish pre-flight checks",
		Commands: []*cli.Command{
			npmDeployCmd(),
			npmValidateTagCmd(),
			npmValidateTarballCmd(),
			npmValidateVersionCmd(),
			npmFindTarballCmd(),
			npmWriteNPMRCCmd(),
		},
	}
}

type explicitNPMRegistry struct{ registry provider.ForgeNPMRegistry }

func (r explicitNPMRegistry) ResolveForgeNPMRegistry() (provider.ForgeNPMRegistry, error) {
	return r.registry, nil
}

func npmDeployCmd() *cli.Command {
	return &cli.Command{
		Name:  commandDeploy,
		Usage: "publish one packed npm tarball to an explicit registry under a version-specific tag",
		Flags: []cli.Flag{
			commonflags.WorkingDir("directory containing exactly one packed .tgz"),
			&cli.StringFlag{Name: flagRegistry, Required: true, Sources: cli.EnvVars("NPM_REGISTRY"), Usage: "destination npm registry URL"},
			&cli.StringFlag{Name: flagNPMTag, Sources: cli.EnvVars("NPM_TAG"), Usage: "required release-{version}, release.{version}, release-<packed-version> or release.<packed-version>"},
			&cli.StringFlag{Name: "token-file", Usage: "registry token file; defaults to $NODE_AUTH_TOKEN"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if err := domainpublish.ValidateNPMTag(cmd.String("npm-tag")); err != nil {
				return err
			}

			if err := apppublish.WriteNPMRC(apppublish.NPMRCInput{Registry: cmd.String("registry"), Output: io.Discard}); err != nil {
				return err
			}

			token, err := secret.Resolve(cmd.String("token-file"), "NODE_AUTH_TOKEN")
			if err != nil {
				return err
			}

			if token == "" {
				return errs.CredentialRequired(errs.Credential{What: "npm registry token", Flag: "token-file", Env: "NODE_AUTH_TOKEN"})
			}

			return apppublish.ForgePackagesNPMPublish(ctx, npm.New(), explicitNPMRegistry{provider.ForgeNPMRegistry{Registry: cmd.String("registry"), Token: token}}, os.Stderr, os.Stderr, apppublish.ForgePackagesNPMPublishInput{WorkingDir: cmd.String("working-dir"), Tag: cmd.String("npm-tag")})
		},
	}
}

func npmValidateTagCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate-tag",
		Usage: "validate a required version-specific npm tag before build or authentication; with --version, print the resolved tag",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagNPMTag, Sources: cli.EnvVars("NPM_TAG"), Usage: "release-{version}, release.{version}, release-<package-version> or release.<package-version>; movable channels are forbidden"},
			&cli.StringFlag{Name: flagVersion, Usage: "package version to bind the tag to"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if cmd.String("version") == "" {
				return domainpublish.ValidateNPMTag(cmd.String("npm-tag"))
			}

			tag, err := domainpublish.NPMVersionTag(cmd.String("version"), cmd.String("npm-tag"))
			if err != nil {
				return err
			}

			_, err = fmt.Fprintln(cmd.Root().Writer, tag)

			return err
		},
	}
}

func npmWriteNPMRCCmd() *cli.Command {
	return &cli.Command{
		Name:  "write-npmrc",
		Usage: "compose a .npmrc with registry + scope, emitting the literal ${NODE_AUTH_TOKEN} placeholder",
		Description: `EXAMPLE:
   forge-ci publish npm write-npmrc --registry https://registry.npmjs.org --scope @examplescope --output .npmrc`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagRegistry, Required: true, Sources: cli.EnvVars("REGISTRY", "NPM_REGISTRY"), Usage: "npm registry URL written into the .npmrc"},
			&cli.StringFlag{Name: "scope", Sources: cli.EnvVars("SCOPE", "PACKAGE_SCOPE"), Usage: "package scope (e.g. @examplescope) routed to the registry"},
			&cli.StringFlag{Name: "output", Sources: cli.EnvVars("NPMRC_OUTPUT"), Usage: "destination path; '-' writes to stdout (default: stdout)"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			path := cmd.String("output")
			if path == "" {
				path = cliio.StdSentinel
			}

			w, err := cliio.CreateWriter(path, 0o600) //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
			if err != nil {
				return err
			}

			defer func() { _ = w.Close() }()

			return apppublish.WriteNPMRC(apppublish.NPMRCInput{
				Registry: cmd.String("registry"),
				Scope:    cmd.String("scope"),
				Output:   w,
			})
		},
	}
}

func npmFindTarballCmd() *cli.Command {
	return &cli.Command{
		Name:  "find-tarball",
		Usage: "find a top-level npm tarball and emit tarball",
		Description: `EXAMPLE:
   forge-ci publish npm find-tarball --working-dir .`,
		Flags: []cli.Flag{
			commonflags.WorkingDir("directory scanned for the npm tarball (where 'npm pack' ran)"),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				annot := deps.Annotator(cmd)
				_, err := apppublish.FindArtifact(ctx, d.OutputSink, os.Stderr, annot, apppublish.FindArtifactInput{
					Dir:       cmd.String("working-dir"),
					Exts:      []string{".tgz", ".tar.gz"},
					OutputKey: "tarball",
					Label:     "npm tarball",
				})

				return err
			})
		},
	}
}

func npmValidateVersionCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate-version",
		Usage: "validate that package@version is unpublished (fails if the version already exists in the registry)",
		Description: `EXAMPLE:
   forge-ci publish npm validate-version --name @examplescope/app --version 1.2.3`,
		Flags: []cli.Flag{
			commonflags.WorkingDir("directory containing package.json (used when --name is omitted)"),
			&cli.StringFlag{Name: "name", Sources: cli.EnvVars("PACKAGE_NAME"), Usage: "package name (defaults to the value in package.json)"},
			&cli.StringFlag{Name: flagVersion, Sources: cli.EnvVars("VERSION"), Usage: "package version to check against the registry"},
			&cli.StringFlag{Name: "registry", Sources: cli.EnvVars("NPM_REGISTRY"), Usage: "npm registry to query"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				annot := deps.Annotator(cmd)

				return apppublish.NPMCheckVersion(ctx, npm.New(), d.OutputSink, os.Stderr, annot, apppublish.NPMCheckVersionInput{
					Dir:      cmd.String("working-dir"),
					Name:     cmd.String("name"),
					Version:  cmd.String("version"),
					Registry: cmd.String("registry"),
				})
			})
		},
	}
}

func npmValidateTarballCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate-tarball",
		Usage: "extract the npm tarball produced by `npm pack`, verify dist/cli.js is present",
		Description: `EXAMPLE:
   # Run in the directory holding the packed tarball (takes no flags)
   forge-ci publish npm validate-tarball`,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			annot := deps.Annotator(cmd)

			return apppublish.NPMValidateTarball(ctx, os.Stderr, os.Stderr, annot, apppublish.NPMValidateTarballInput{})
		},
	}
}
