// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/urfave/cli/v3"
)

func releaseBuildInputFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "build-json", Required: true, Usage: "one build entry from the admitted release image plan"},
		&cli.StringFlag{Name: flagReleaseSHA, Required: true, Usage: "prepared release source commit"},
		&cli.StringFlag{Name: flagServerURL, Required: true, Usage: "source forge URL"},
		&cli.StringFlag{Name: flagRepository, Required: true, Usage: "source owner/repository"},
		&cli.StringFlag{Name: flagInputsDir, Required: true, Usage: "relative run-scoped directory holding only fetched build inputs"},
	}
}

func releaseBuildFromJSON(cmd *cli.Command) (pipeline.ReleaseImageBuild, error) {
	var build pipeline.ReleaseImageBuild

	decoder := json.NewDecoder(strings.NewReader(cmd.String("build-json")))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&build); err != nil {
		return build, fmt.Errorf("invalid planned image build: %w", errs.ErrUsage)
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return build, fmt.Errorf("planned image build must contain exactly one JSON object: %w", errs.ErrUsage)
	}

	if err := pipeline.ValidateRegistryInputs(build); err != nil {
		return build, err
	}

	if !filepath.IsLocal(cmd.String(flagInputsDir)) {
		return build, fmt.Errorf("inputs-dir must be a safe relative directory: %w", errs.ErrUsage)
	}

	return build, nil
}

func releaseImagesFetchBuildInputsCmd() *cli.Command {
	return &cli.Command{
		Name: "fetch-build-inputs", Usage: "fetch planned cache and pinned parents without running consumer code",
		Description: "--inputs-dir must not exist yet; its missing parents are created, so a build's inputs/<name>-arch-<arch> can be named directly.",
		Flags:       append(releaseBuildInputFlags(), regflags.AuthFile(regflags.AuthFileOpts{})),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			build, err := releaseBuildFromJSON(cmd)
			if err != nil {
				return err
			}

			input := releaseImageBuildInput(cmd, build)
			input.AuthFile = cmd.String(flagAuthFile)

			registry := ociregistry.NewAnonymous()
			if input.AuthFile != "" {
				registry = ociregistry.WithAuthFile(input.AuthFile)
			}

			return appcontainer.FetchReleaseBuildInputs(ctx, registry, os.Stderr, input, build.RegistryInputs, cmd.String(flagInputsDir))
		},
	}
}

func releaseImagesBuildPreparedCmd() *cli.Command {
	return &cli.Command{
		Name: "build-prepared", Usage: "import verified parents and build using only secret-free prepared registry inputs",
		Flags: append(append(releaseBuildInputFlags(), imageBuilderFlags()...),
			&cli.StringFlag{Name: flagReleaseTag, Required: true, Usage: "prepared release tag"},
			&cli.StringFlag{Name: flagCreated, Required: true, Usage: "source commit timestamp"},
			&cli.StringFlag{Name: flagSourceDir, Value: "source", Usage: "consumer checkout"},
			&cli.StringFlag{Name: flagMetadataDir, Value: ".", Usage: "relative architecture layout parent directory"}),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			build, err := releaseBuildFromJSON(cmd)
			if err != nil {
				return err
			}

			builder, err := imageBuilderFromCmd(cmd)
			if err != nil {
				return err
			}

			if err := validateReleaseImageBuilder(builder, build, true); err != nil {
				return err
			}

			return stagePreparedReleaseImageBuild(ctx, cmd, builder, build, cmd.String(flagInputsDir))
		},
	}
}

// Both job verbs use the same selected-builder policy. The native batch calls
// this for its whole share before executing any build; the app rechecks identity
// at the actual build/cache-import boundary.
func validateReleaseImageBuilder(builder imageBuilder, build pipeline.ReleaseImageBuild, prepared bool) error {
	if err := appcontainer.CheckSignerImageBuilderIdentity(signerImageTool{builder}, build.BuilderMaterialID); err != nil {
		return err
	}

	if len(build.RegistryInputs) == 0 {
		return nil
	}

	if !prepared {
		return fmt.Errorf("release image %s requires secret-free fetched parents: pass --inputs-dir: %w", build.Name, errs.ErrUsage)
	}

	if _, ok := builder.(*buildah.Adapter); !ok {
		return fmt.Errorf("prepared private parents require Buildah: %w", errs.ErrUsage)
	}

	return nil
}

func stagePreparedReleaseImageBuild(ctx context.Context, cmd *cli.Command, builder imageBuilder, build pipeline.ReleaseImageBuild, directory string) error {
	loader, _ := builder.(*buildah.Adapter)
	_, err := appcontainer.StageReleaseBuildWithInputs(ctx, signerImageTool{builder}, loader, ociregistry.NewAnonymous(), os.Stderr, releaseImageBuildInput(cmd, build), build.RegistryInputs, directory)

	return err
}
