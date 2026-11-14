// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"os"
	"runtime"

	"github.com/diggsweden/forge-ci/internal/adapters/apt"
	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/urfave/cli/v3"
)

func setupImageBuilderCmd() *cli.Command {
	command := setupBuildahCmd()
	command.Name = "setup-image-builder"
	command.Usage = "set up Buildah or admit a digest-pinned native Kaniko job image"
	command.Description = "Reuses setup-buildah for local storage and package setup. Kaniko is already provisioned in the job image: verify its digest, native architecture and engine source/title labels without installing Buildah."
	command.Flags = append(command.Flags,
		&cli.BoolFlag{Name: "require-job-image", Usage: "require a digest-pinned job image for either backend (GitLab release policy)"},
		&cli.StringFlag{Name: flagImageBuilder, Value: imageBuilderBuildah, Sources: cli.EnvVars(envImageBuilder), Usage: imageBuilderUsage},
		&cli.StringFlag{Name: "job-image", Sources: cli.EnvVars("CI_IMAGE_BUILDER_JOB_IMAGE"), Usage: "Kaniko job image pinned by digest"},
		&cli.StringFlag{Name: "arch", Value: runtime.GOARCH, Usage: "architecture the Kaniko job image must serve: the job's build architecture (default: this binary's)"},
		&cli.StringFlag{Name: "engine-repository", Sources: cli.EnvVars("FORGE_CI_REPOSITORY_URL"), Usage: "trusted engine source repository HTTPS URL"},
		regflags.AuthFile(regflags.AuthFileOpts{Usage: "registry auth file for Kaniko job-image inspection"}),
	)
	command.Action = func(ctx context.Context, cmd *cli.Command) error {
		return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
			return appcontainer.SetupImageBuilder(ctx, buildah.New(), apt.New(), ociregistry.WithAuthFile(cmd.String(flagAuthFile)), dep.OutputSink, dep.SummarySink, os.Stderr, appcontainer.SetupImageBuilderInput{
				Builder: cmd.String(flagImageBuilder), JobImage: cmd.String("job-image"),
				EngineRepository: cmd.String("engine-repository"), Arch: cmd.String("arch"), Buildah: setupBuildahInput(cmd), RequireJobImage: cmd.Bool("require-job-image"),
			})
		})
	}

	return command
}
