// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/distroregistry"
	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	flagArtifactsFile        = "file"
	flagManifest             = "manifest"
	flagAdmit                = "admit"
	flagTokenConfigured      = "token-configured"
	outputDistroPackages     = "distro-packages"
	defaultArtifactsFile     = ".forge-ci/artifacts.yml"
	defaultDistroPackagesDir = "dist"
)

func forgePackagesDistroPackagesCmd() *cli.Command {
	return &cli.Command{
		Name:  "distro-packages",
		Usage: "publish a signed release's .deb, .rpm and .apk files to the forge's Debian, RPM and Alpine registries",
		Description: `Publishes the .deb, .rpm and .apk files the finalized release manifest names
   to the forge's own package registries, when artifacts.yml declares
   distro-packages; without the block it publishes nothing and succeeds. The
   places come from the block: distribution and component for Debian, the
   group for RPM, branch and repository for Alpine.

   Each file is sent as the release holds it, after the release's checksums
   are verified against it, and the registry is then read back: it has to
   serve that file's SHA-256 at its place. A package an earlier run published
   is left as it is when its content is this file's; the same package version
   with other content fails. Nothing is replaced or deleted.

   The registry and its token are the detected forge's (override with
   --provider). Forgejo has these registries; another forge is refused.

   With --admit nothing is sent and no token is read: the release is checked
   before it is signed and published. The output distro-packages says
   whether the block is declared. --token-configured false refuses a
   declared block, and with --manifest, which may be the unsigned one, every
   package among the release's files has to be publishable and the forge's
   registry has to be one a package is sent to: HTTPS.

EXAMPLE:
   forge-ci publish forge-packages distro-packages --manifest dist/release-files.json --dist-dir dist
   forge-ci publish forge-packages distro-packages --admit --token-configured true
   forge-ci publish forge-packages distro-packages --admit --manifest dist/release-files.json --dist-dir dist`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagArtifactsFile, Value: defaultArtifactsFile, Usage: "declaration that holds the distro-packages block"},
			&cli.StringFlag{Name: flagManifest, Usage: "release file manifest: the finalized one of the signed release, required without --admit"},
			&cli.StringFlag{Name: flagDistDir, Value: defaultDistroPackagesDir, Usage: "directory holding the release's files"},
			&cli.BoolFlag{Name: flagAdmit, Usage: "check that the release can publish its packages, and send nothing"},
			&cli.StringFlag{Name: flagTokenConfigured, Usage: "with --admit: true or false, whether the package token is configured"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return publishDistroPackages(ctx, cmd, d)
			})
		},
	}
}

func publishDistroPackages(ctx context.Context, cmd *cli.Command, dependencies *deps.Deps) error {
	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: cmd.String(flagArtifactsFile)}, deps.Annotator(cmd), true)
	if err != nil {
		return err
	}

	admit := cmd.Bool(flagAdmit)
	files := apprelease.FilesInput{DistDir: cmd.String(flagDistDir), ManifestFile: cmd.String(flagManifest), ManifestExplicit: true}

	if !admit && files.ManifestFile == "" {
		return fmt.Errorf("--manifest is required without --admit: %w", errs.ErrUsage)
	}

	if plans.DistroPackages == nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s declares no distro-packages: no package is published to a registry\n", cmd.String(flagArtifactsFile))

		if !admit {
			return nil
		}

		return dependencies.OutputSink.Set(ctx, outputDistroPackages, "false")
	}

	if admit {
		if err := admitDistroPackages(cmd, dependencies, files, *plans.DistroPackages); err != nil {
			return err
		}

		return dependencies.OutputSink.Set(ctx, outputDistroPackages, "true")
	}

	// Before the release's files are read: a forge without these registries
	// is refused whatever the release holds.
	resolver, err := dependencies.RequireForgeDistroRegistryResolver()
	if err != nil {
		return err
	}

	// The files `release publish` uploads, held to the release's checksums:
	// what goes to the registry is what the release signed.
	paths, err := apprelease.PublishableReleaseFiles(files)
	if err != nil {
		return err
	}

	if err := apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{FilesInput: files}); err != nil {
		return err
	}

	return apppublish.ForgePackagesDistroPublish(ctx, distroregistry.Client{}, resolver, os.Stderr, apppublish.ForgePackagesDistroInput{
		Files:  paths,
		Places: *plans.DistroPackages,
	})
}

// admitDistroPackages refuses, before the release is signed or published,
// a declared block whose token is not configured and a release file that
// cannot be published.
func admitDistroPackages(cmd *cli.Command, dependencies *deps.Deps, files apprelease.FilesInput, places config.DistroPackages) error {
	switch cmd.String(flagTokenConfigured) {
	case "", "true":
	case "false":
		return fmt.Errorf("%s declares distro-packages, but the token that may write the owner's packages is not configured (RELEASE_REGISTRY_TOKEN in the Forgejo release): %w",
			cmd.String(flagArtifactsFile), errs.ErrPermissionDenied)
	default:
		return fmt.Errorf("--%s takes true or false: %w", flagTokenConfigured, errs.ErrUsage)
	}

	if files.ManifestFile == "" {
		return nil
	}

	// The registry's address is known without a token: one the publisher
	// would not send a package to is refused here, not after the release.
	resolver, err := dependencies.RequireForgeDistroRegistryResolver()
	if err != nil {
		return err
	}

	registry, err := resolver.ResolveForgeDistroRegistry()
	if err != nil {
		return err
	}

	if err := distroregistry.CheckRegistry(registry); err != nil {
		return err
	}

	paths, err := apprelease.ListReleaseFiles(apprelease.ListReleaseFilesInput{FilesInput: files})
	if err != nil {
		return err
	}

	count, err := apppublish.ForgePackagesDistroAdmit(apppublish.ForgePackagesDistroInput{Files: paths, Places: places})
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(os.Stderr, "%d distro packages of the release can be published\n", count)

	return err
}
