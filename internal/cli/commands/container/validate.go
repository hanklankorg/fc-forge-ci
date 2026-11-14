// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"cmp"
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
)

// validateGroup wires `forge-ci container validate <verb>` —
// per-aspect container validation (artifact presence, Containerfile
// path resolution, registry-namespace policy).
func validateGroup() *cli.Command {
	return &cli.Command{
		Name:  subCmdValidate,
		Usage: "validate one aspect of the container build inputs",
		Commands: []*cli.Command{
			validateArtifactsCmd(),
			validateRebuildsCmd(),
			validateImageNamesCmd(),
			validateContainerfileCmd(),
			validateNamespaceCmd(),
		},
	}
}

func validateArtifactsCmd() *cli.Command {
	return &cli.Command{
		Name:  "artifacts",
		Usage: "verify project-type-specific artifacts are present",
		Description: `EXAMPLE:
   forge-ci container validate artifacts --project-type maven --artifact-dir target`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "project-type",
				Required: true,
				Sources:  cli.EnvVars("PROJECT_TYPE"),
				Usage:    "ecosystem of the produced artifacts (maven/gradle/go/cargo)",
			},
			&cli.StringFlag{
				Name:     "artifact-dir",
				Required: true,
				Sources:  cli.EnvVars("ARTIFACT_DIR"),
				Usage:    "directory holding the built artifacts to inspect",
			},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			annot := deps.Annotator(cmd)

			return appcontainer.ValidateArtifacts(os.Stderr, annot, appcontainer.ValidateArtifactsInput{
				ProjectType: cmd.String("project-type"),
				ArtifactDir: cmd.String("artifact-dir"),
			})
		},
	}
}

func validateRebuildsCmd() *cli.Command {
	return &cli.Command{
		Name:  "rebuilds",
		Usage: "warn when a declared container's Containerfile rebuilds an artifact forge-ci built",
		Description: `Reads the containers of artifacts.yml that are built from an artifact (from:)
and the RUN instructions of their Containerfiles, in the working directory. A
Containerfile that runs the artifact's build again (./mvnw package, npm run
build, go build and the like) gets a warning: the image would hold a rebuilt
file, not the artifact forge-ci built, tested, described in the SBOM and
released. The command warns and exits 0; a missing artifacts.yml is nothing
to check.

EXAMPLE:
   forge-ci container validate rebuilds`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "file",
				Value:   defaultArtifactsFile,
				Sources: cli.EnvVars("ARTIFACTS_CONFIG"),
				Usage:   "path to the artifacts.yml that declares the containers",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				appconfig.WarnContainerfileRebuilds(ctx, os.Stderr, deps.Annotator(cmd), d.SummarySink,
					appconfig.EmitConfigPlanInput{Path: cmp.Or(cmd.String("file"), defaultArtifactsFile)})

				return nil
			})
		},
	}
}

func validateImageNamesCmd() *cli.Command {
	return &cli.Command{
		Name:  "image-names",
		Usage: "warn when a declared container's Containerfile names an image without its registry",
		Description: `Reads the Containerfile of each container of artifacts.yml, in the working
directory, and the images it names in FROM, COPY --from= and RUN --mount. An
image named without a registry (eclipse-temurin:21, not
docker.io/library/eclipse-temurin:21) gets a warning: the registries.conf of
the runner decides where it is pulled from, so the build fails, or pulls
another image, on another runner. scratch and an earlier build stage are not
images. A name with a build argument that has no default in the file is
named as not checked. The command warns and exits 0; a missing artifacts.yml
is nothing to check.

EXAMPLE:
   forge-ci container validate image-names`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "file",
				Value:   defaultArtifactsFile,
				Sources: cli.EnvVars("ARTIFACTS_CONFIG"),
				Usage:   "path to the artifacts.yml that declares the containers",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				appconfig.WarnContainerfileShortImageNames(ctx, os.Stderr, deps.Annotator(cmd), d.SummarySink,
					appconfig.EmitConfigPlanInput{Path: cmp.Or(cmd.String("file"), defaultArtifactsFile)})

				return nil
			})
		},
	}
}

func validateContainerfileCmd() *cli.Command {
	return &cli.Command{
		Name:  flagContainerfile,
		Usage: "verify a Containerfile path exists (or glob-resolves uniquely), emit containerfile=<path>",
		Description: `EXAMPLE:
   forge-ci container validate containerfile --path Containerfile`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "path",
				Required: true,
				Sources:  cli.EnvVars("CONTAINERFILE"),
				Usage:    "Containerfile path or glob (e.g. Containerfile, src/*Containerfile)",
			},
			&cli.StringFlag{Name: "context", Usage: "also require this context and the resolved Containerfile to stay inside the current source directory"},
			&cli.StringSliceFlag{Name: "source-dir", Usage: "also require this directory to be a relative path with no '..' segment (repeatable)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				in := appcontainer.ValidateContainerfileInput{Path: cmd.String("path"), SourceDirs: cmd.StringSlice("source-dir")}
				if cmd.IsSet("context") {
					value := cmd.String("context")
					in.Context = &value
				}

				return appcontainer.ValidateContainerfile(ctx, d.OutputSink, os.Stderr, in)
			})
		},
	}
}

func validateNamespaceCmd() *cli.Command {
	return &cli.Command{
		Name:  "namespace",
		Usage: "verify an image lives in the allowed namespace for an enforced registry (no-op for registries not in --enforce-namespace-on)",
		Description: `EXAMPLE:
   forge-ci container validate namespace --image-name ghcr.io/owner/app \
     --repository org/app --registry ghcr.io --enforce-namespace org`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     flagImageName,
				Required: true,
				Sources:  cli.EnvVars("IMAGE_NAME"),
				Usage:    "image ref (registry/owner/name) whose namespace is checked",
			},
			&cli.StringFlag{
				Name:     flagRepository,
				Required: true,
				Sources:  cienv.Repository(),
				Usage:    "\"owner/repo\" the image must be namespaced under on an enforced registry",
			},
			&cli.StringFlag{
				Name:     "registry",
				Required: true,
				Sources:  cli.EnvVars("CONTAINER_REGISTRY"),
				Usage:    "registry hostname the image is pushed to (checked against --enforce-namespace-on)",
			},
			&cli.StringFlag{
				Name:     "enforce-namespace",
				Required: true,
				Sources:  cli.EnvVars("ENFORCE_NAMESPACE"),
				Usage:    "required namespace prefix (e.g. the owner) the image name must start with",
			},
			&cli.StringSliceFlag{
				Name:    "enforce-namespace-on",
				Value:   []string{domaincontainer.DefaultRegistry},
				Sources: cli.EnvVars("ENFORCE_NAMESPACE_ON"),
				Usage:   "registries whose namespace policy this deployment enforces (default ghcr.io); set to your registry when self-hosting so the check runs instead of silently passing",
			},
			&cli.StringFlag{
				Name:    flagExpectedImageRepository,
				Sources: cli.EnvVars("EXPECTED_IMAGE_REPOSITORY"),
				Usage:   "explicit allowed OCI repository base, constrained to the enforced registry/namespace; set independently of image-name for source names requiring a mapping",
			},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			err := domaincontainer.ValidateNamespace(domaincontainer.ValidateNamespaceInput{
				ImageName:               cmd.String(flagImageName),
				Repository:              cmd.String(flagRepository),
				Registry:                cmd.String("registry"),
				EnforceNamespace:        cmd.String("enforce-namespace"),
				EnforceOnRegistries:     cmd.StringSlice("enforce-namespace-on"),
				ExpectedImageRepository: cmd.String(flagExpectedImageRepository),
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(os.Stderr, "✓ Image namespace validated: %s\n", cmd.String(flagImageName))

			return nil
		},
	}
}
