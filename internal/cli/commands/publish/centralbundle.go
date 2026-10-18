// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/central"
	"github.com/diggsweden/forge-ci/internal/adapters/gpg"
	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/cli/commonflags"
	"github.com/diggsweden/forge-ci/internal/cli/secret"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/urfave/cli/v3"
)

func publicationFileFlags() []cli.Flag {
	return []cli.Flag{
		commonflags.WorkingDir("exported Maven-layout publication directory"),
		&cli.StringFlag{Name: flagVersion, Required: true, Sources: cli.EnvVars("VERSION"), Usage: "expected immutable publication version; leading v allowed"},
	}
}

func checkedPublicationVersion(cmd *cli.Command) (string, error) {
	want := strings.TrimPrefix(cmd.String("version"), "v")

	tag, ok := version.ReleaseRequestVersion(strings.TrimPrefix(cmd.String("request-tag"), "refs/tags/"))
	if !ok || tag != "v"+want {
		return "", fmt.Errorf("publication version must match the signed release request: %w", errs.ErrPermissionDenied)
	}

	return want, nil
}

func mavenPublicationValidateCmd() *cli.Command {
	return &cli.Command{
		Name: "validate-publication", Usage: "validate all exported JVM publications and Central metadata before importing signing keys",
		Flags: publicationFileFlags(),
		Action: func(_ context.Context, cmd *cli.Command) error {
			set, err := apppublish.InspectMavenPublication(cmd.String("working-dir"), strings.TrimPrefix(cmd.String("version"), "v"), true)
			if err != nil {
				return err
			}

			_, err = fmt.Fprintf(os.Stderr, "Validated %d publication(s)\n", len(set.Publications))

			return err
		},
	}
}

func mavenPublicationSignCmd() *cli.Command {
	flags := append(publicationAuthorizationFlags(), publicationFileFlags()...)
	flags = append(flags,
		&cli.StringFlag{Name: "fingerprint", Required: true, Sources: cli.EnvVars("GPG_FINGERPRINT"), Usage: "imported signing key's primary fingerprint"},
		&cli.StringFlag{Name: "passphrase-file", Usage: "signing passphrase file; defaults to $GPG_PASSPHRASE"})

	return &cli.Command{
		Name: "sign-publication", Usage: "sign exported Maven files without running Gradle or Maven",
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			want, err := checkedPublicationVersion(cmd)
			if err != nil {
				return err
			}

			if authErr := authorizePublication(ctx, cmd); authErr != nil {
				return authErr
			}

			passphrase, err := secret.Resolve(cmd.String("passphrase-file"), "GPG_PASSPHRASE")
			if err != nil {
				return err
			}

			return apppublish.SignMavenPublication(ctx, gpg.New(), os.Stderr, apppublish.MavenSignInput{Directory: cmd.String("working-dir"), Version: want, Fingerprint: cmd.String("fingerprint"), Passphrase: passphrase, PublicKey: cmd.String("release-gpg-public-key")})
		},
	}
}

func centralCredentialFlags() []cli.Flag {
	return append(centralAuthFlags(),
		&cli.BoolFlag{Name: "automatic", Usage: "request/observe automatic publication; success requires PUBLISHED, not just VALIDATED"},
		&cli.DurationFlag{Name: "wait-timeout", Value: 30 * time.Minute, Usage: "maximum observation time; timeout never resubmits the bundle"},
	)
}

func centralAuthFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "username-file", Usage: "Central token username file; defaults to $MAVEN_CENTRAL_USERNAME"},
		&cli.StringFlag{Name: "password-file", Usage: "Central token password file; defaults to $MAVEN_CENTRAL_PASSWORD"},
	}
}

func centralClient(cmd *cli.Command) (central.Client, error) {
	var client central.Client
	if cmd.Duration("wait-timeout") <= 0 {
		return client, fmt.Errorf("--wait-timeout must be positive: %w", errs.ErrUsage)
	}

	var err error

	client.Username, client.Password, err = centralCredentials(cmd)

	return client, err
}

func centralCredentials(cmd *cli.Command) (string, string, error) {
	username, err := secret.Resolve(cmd.String("username-file"), "MAVEN_CENTRAL_USERNAME")
	if err != nil {
		return "", "", err
	}

	password, err := secret.Resolve(cmd.String("password-file"), "MAVEN_CENTRAL_PASSWORD")
	if err != nil {
		return "", "", err
	}

	if username == "" || password == "" {
		return "", "", fmt.Errorf("central token username and password are required: %w", errs.ErrPermissionDenied)
	}

	return username, password, nil
}

func centralBundleCmd() *cli.Command {
	flags := append(publicationAuthorizationFlags(), publicationFileFlags()...)
	flags = append(flags, centralCredentialFlags()...)

	return &cli.Command{
		Name: "bundle", Usage: "verify and submit a signed Maven-layout bundle once, then observe its Central deployment",
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			want, err := checkedPublicationVersion(cmd)
			if err != nil {
				return err
			}

			if authErr := authorizePublication(ctx, cmd); authErr != nil {
				return authErr
			}

			client, err := centralClient(cmd)
			if err != nil {
				return err
			}

			return apppublish.SubmitCentralBundle(ctx, client, os.Stderr, apppublish.CentralBundleInput{Directory: cmd.String("working-dir"), Version: want, PublicKey: cmd.String("release-gpg-public-key"), Automatic: cmd.Bool("automatic"), WaitTimeout: cmd.Duration("wait-timeout")})
		},
	}
}

func centralWaitCmd() *cli.Command {
	return &cli.Command{
		Name: "wait", Usage: "resume observation of a Central deployment by ID; never uploads or releases anything",
		Flags: append(centralCredentialFlags(), &cli.StringFlag{Name: "deployment-id", Required: true, Usage: "deployment ID printed by the accepted submission"}),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			client, err := centralClient(cmd)
			if err != nil {
				return err
			}

			return apppublish.WaitCentralDeployment(ctx, client, os.Stderr, cmd.String("deployment-id"), cmd.Bool("automatic"), cmd.Duration("wait-timeout"), 0)
		},
	}
}
