// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/maven"
	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/cli/commonflags"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/safeexec"
	"github.com/diggsweden/forge-ci/internal/secretenv"
)

func snapshotMavenAdapter() *maven.Adapter {
	return &maven.Adapter{Env: safeexec.EnvWithout(os.Environ(), secretenv.Names("MAVEN_ARGS", "MAVEN_CENTRAL_PASSWORD", "DEPENDENCY_TOKEN", "RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_PASSPHRASE")...)}
}

func mavenModelAdapter() *maven.Adapter {
	// Preserve project-affecting options exactly as deploy sees them. Only the
	// isolated deploy-file path clears MAVEN_ARGS; the reactor model must not.
	return &maven.Adapter{Env: safeexec.EnvWithout(os.Environ(), secretenv.Names("MAVEN_CENTRAL_PASSWORD", "RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_PASSPHRASE")...)}
}

func centralSnapshotCmd() *cli.Command {
	return &cli.Command{
		Name: "snapshot", Usage: "upload exported Maven-format snapshots using native metadata handling, without running consumer project code",
		Flags: append(centralAuthFlags(), commonflags.WorkingDir("exported snapshot publication directory"), &cli.BoolFlag{Name: "validate-only", Usage: "validate exported snapshots without credentials or upload"}),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Bool("validate-only") {
				_, err := apppublish.InspectMavenSnapshot(cmd.String("working-dir"), "")

				return err
			}

			username, password, err := centralCredentials(cmd)
			if err != nil {
				return err
			}

			registry := provider.ForgeMavenRegistry{ServerID: "central-snapshots", URL: "https://central.sonatype.com/repository/maven-snapshots/", AuthScheme: provider.MavenAuthServerPassword, Username: username, Token: password}

			return apppublish.UploadMavenSnapshots(ctx, snapshotMavenAdapter(), registry, os.Stderr, cmd.String("working-dir"))
		},
	}
}
