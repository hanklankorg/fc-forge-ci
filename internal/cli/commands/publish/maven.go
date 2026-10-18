// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/adapters/maven"
	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

func mavenCentralCmd() *cli.Command {
	return &cli.Command{
		Name:  "maven-central",
		Usage: "maven central pre-flight checks",
		Commands: []*cli.Command{
			mavenCentralAuthorizeCmd(),
			mavenPublicationValidateCmd(),
			mavenPublicationSignCmd(),
			centralBundleCmd(),
			centralWaitCmd(),
			centralSnapshotCmd(),
			mavenCentralValidateArtifactsCmd(),
			mavenCentralDeployCmd(),
		},
	}
}

func publicationAuthorizationFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "request-tag", Sources: cli.EnvVars("RELEASE_REQUEST_TAG"), Usage: "signed human release-request/vMAJOR.MINOR.PATCH tag"},
		&cli.StringFlag{Name: "release-sha", Sources: cli.EnvVars("RELEASE_SHA"), Usage: "full prepared release commit SHA"},
		&cli.StringFlag{Name: "authorization-dir", Value: ".", Sources: cli.EnvVars("FORGE_CI_AUTHORIZATION_DIR"), Usage: "reviewed checkout root holding .forge-ci signer allowlists"},
		&cli.StringFlag{Name: "release-gpg-public-key", Sources: cli.EnvVars("RELEASE_GPG_PUBLIC_KEY"), Usage: "public key verifying the prepared release tag when GPG-signed"},
	}
}

func authorizePublication(ctx context.Context, cmd *cli.Command) error {
	dir := cmd.String("authorization-dir")

	return appvalidate.PublicationRequest(ctx, &gitadapter.Repo{Dir: dir}, os.Stderr, deps.Annotator(cmd), appvalidate.PublicationRequestInput{
		RequestTag: cmd.String("request-tag"), ReleaseSHA: cmd.String("release-sha"), AuthorizationDir: dir, ReleaseGPGPublicKey: cmd.String("release-gpg-public-key"),
	})
}

func mavenCentralAuthorizeCmd() *cli.Command {
	return &cli.Command{
		Name:   "authorize",
		Usage:  "verify the signed request and prepared source before importing keys or executing project code",
		Flags:  publicationAuthorizationFlags(),
		Action: authorizePublication,
	}
}

func mavenCentralDeployCmd() *cli.Command {
	return &cli.Command{
		Name:  commandDeploy,
		Usage: "validate settings.xml (if set) and run `mvn deploy -P<profile> -DskipTests`",
		Description: `EXAMPLE:
   forge-ci publish maven-central deploy --profile release --settings-path settings.xml`,
		Flags: append(publicationAuthorizationFlags(), []cli.Flag{
			&cli.BoolFlag{Name: "validate-only", Usage: "authorize and validate the resolved reactor without deploying; suitable before importing signing keys"},
			&cli.BoolFlag{Name: flagSnapshot, Sources: cli.EnvVars("MAVEN_SNAPSHOT"), Usage: "deploy only a resolved all-SNAPSHOT reactor using ordinary CI credentials; no signed release request required"},
			&cli.StringFlag{Name: "cli-opts", Sources: cli.EnvVars("MAVEN_CLI_OPTS"), Usage: "diagnostic Maven flags and -Dproperties, whitespace-separated; use dedicated settings/profile flags"},
			&cli.StringFlag{Name: "settings-path", Sources: cli.EnvVars("SETTINGS_PATH"), Usage: "path to the settings.xml passed via -s"},
			&cli.StringFlag{Name: "profile", Required: true, Sources: cli.EnvVars("MAVEN_PROFILE"), Usage: "Maven profile activated for the deploy (e.g. release)"},
		}...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if !cmd.Bool("snapshot") {
				if err := authorizePublication(ctx, cmd); err != nil {
					return err
				}
			}

			input := apppublish.MavenCentralDeployInput{
				CLIOpts:      strings.Fields(cmd.String("cli-opts")),
				SettingsPath: cmd.String("settings-path"),
				Profile:      cmd.String("profile"),
			}
			if cmd.Bool(flagSnapshot) {
				input.CLIOpts = append(input.CLIOpts, "-Dgpg.skip=true")
			}

			tag, _ := version.ReleaseRequestVersion(strings.TrimPrefix(cmd.String("request-tag"), "refs/tags/"))

			if err := apppublish.ValidateMavenDeployment(ctx, mavenModelAdapter(), os.Stderr, input, cmd.Bool(flagSnapshot), strings.TrimPrefix(tag, "v")); err != nil {
				return err
			}

			if cmd.Bool("validate-only") {
				return nil
			}

			// Native deployment executes trusted project code with credentials.
			// Re-resolve with that exact environment: credentials can activate
			// profiles, so the secret-free preflight is not the final version gate.
			deployment := &maven.Adapter{Env: os.Environ()}
			if err := apppublish.ValidateMavenDeployment(ctx, deployment, os.Stderr, input, cmd.Bool(flagSnapshot), strings.TrimPrefix(tag, "v")); err != nil {
				return err
			}

			return apppublish.MavenCentralDeploy(ctx, deployment, os.Stderr, os.Stderr, input)
		},
	}
}

func mavenCentralValidateArtifactsCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate-artifacts",
		Usage: "verify sources + javadoc JARs are present under */target/ before deploying to Maven Central",
		Description: `EXAMPLE:
   # Run from the build root after a Maven build (takes no flags)
   forge-ci publish maven-central validate-artifacts`,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			annot := deps.Annotator(cmd)
			_, err := apppublish.MavenValidateArtifacts(ctx, os.Stderr, os.Stderr, annot, apppublish.MavenValidateArtifactsInput{})

			return err
		},
	}
}
