// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/adapters/kaniko"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The image builders a job can select by type. Buildah is the default:
// rootless, daemonless, with a local image store for smoke tests. Kaniko is for
// a runner pool that denies the user namespaces Buildah needs; it builds to a
// layout (a local export is extracted from one) and runs in the job image
// CI_IMAGE_BUILDER_JOB_IMAGE names. A new builder is an adapter and one more
// case in newImageBuilder.
const (
	flagImageBuilder     = "image-builder"
	flagKanikoExecutor   = "kaniko-executor"
	imageBuilderBuildah  = "buildah"
	imageBuilderKaniko   = "kaniko"
	envImageBuilder      = "CI_IMAGE_BUILDER"
	envKanikoExecutor    = "KANIKO_EXECUTOR"
	imageBuilderUsage    = "image builder type: buildah (default; rootless, needs user namespaces) or kaniko (for a pool that denies them; runs as root in the job image CI_IMAGE_BUILDER_JOB_IMAGE names, builds to a layout, exports a stage's files in local mode, no load mode, no build secrets)."
	kanikoExecutorUsage  = "path of the kaniko executor (default " + kaniko.DefaultExecutor + ")"
	imageBuilderEnvUsage = " Set " + envImageBuilder + " on the repository to select it."
)

// imageBuilder is the build surface both adapters share: the single-image
// build of `container build` and the two-phase build of `multiarch-image
// build-arch`.
type imageBuilder interface {
	Identity() (domaincontainer.BuilderIdentity, error)
	Build(ctx context.Context, req domaincontainer.BuildRequest, out io.Writer) error
	BuildToLayout(ctx context.Context, req domaincontainer.BuildRequest, layoutDir string, out io.Writer) error
	BuildSignerImage(ctx context.Context, req domaincontainer.SignerImageBuildToolRequest, out io.Writer) error
	ExportLocalImageToLayout(ctx context.Context, imageRef, layoutDir string, out io.Writer) error
}

func builderIdentityCmd() *cli.Command {
	return &cli.Command{
		Name:        "builder-identity",
		Usage:       "print an opaque cache-material ID for the selected image builder without running it",
		Description: "Prints a SHA-256 material ID by default: the consumer includes that value in its cache inputs without interpreting backend names or executable details. --format=json emits the ID and builder details for provenance and diagnostics. Capture on each native build runner. The engine pin and approved job-image digest remain separate provenance inputs. This command performs no build, storage probe, registry operation or installation.",
		Flags:       append(imageBuilderFlags(), &cli.StringFlag{Name: "format", Value: "id", Usage: "output format: id or json"}),
		Action: func(_ context.Context, cmd *cli.Command) error {
			format := cmd.String("format")
			if format != "id" && format != "json" {
				return fmt.Errorf("builder identity format must be id or json: %w", errs.ErrUsage)
			}

			builder, err := imageBuilderFromCmd(cmd)
			if err != nil {
				return err
			}

			identity, err := builder.Identity()
			if err != nil {
				return err
			}

			if format == "id" {
				_, err := fmt.Fprintln(os.Stdout, identity.MaterialID())

				return err
			}

			record := struct {
				ID      string                          `json:"id"`
				Builder domaincontainer.BuilderIdentity `json:"builder"`
			}{ID: identity.MaterialID(), Builder: identity}
			if err := json.NewEncoder(os.Stdout).Encode(record); err != nil {
				return fmt.Errorf("write builder identity: %w", err)
			}

			return nil
		},
	}
}

// imageBuilderFlags declares the builder selection for a build command.
func imageBuilderFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagImageBuilder, Value: imageBuilderBuildah, Sources: cli.EnvVars(envImageBuilder), Usage: imageBuilderUsage + imageBuilderEnvUsage},
		&cli.StringFlag{Name: flagKanikoExecutor, Sources: cli.EnvVars(envKanikoExecutor), Usage: kanikoExecutorUsage},
	}
}

// imageBuilderFromCmd returns the selected adapter.
func imageBuilderFromCmd(cmd *cli.Command) (imageBuilder, error) {
	return newImageBuilder(cmd.String(flagImageBuilder), cmd.String(flagKanikoExecutor))
}

func newImageBuilder(name, kanikoExecutor string) (imageBuilder, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case imageBuilderBuildah, "":
		return buildah.New(), nil
	case imageBuilderKaniko:
		return &kaniko.Adapter{Executor: kanikoExecutor, Filesystem: ociregistry.NewAnonymous()}, nil
	default:
		return nil, fmt.Errorf("--%s must be %s or %s, got %q: %w", flagImageBuilder, imageBuilderBuildah, imageBuilderKaniko, name, errs.ErrUsage)
	}
}
