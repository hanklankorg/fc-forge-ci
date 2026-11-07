// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func baseImagesPromoteCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdPromote,
		Usage: "verify base-image evidence and promote candidate refs to immutable final tags",
		Description: `Verifies each image's signature, CycloneDX SBOM attestation and SLSA lineage
with the trusted key, then makes its candidate the immutable final tag.

Use --ledger to admit and project a publication base-image ledger directly,
or --all-images-json for the existing metadata contract, never both.

Every image must be in the one base-image repository, --expected-repository
or the one derived from --server-url and --repository; the login goes to its
registry.`,
		Flags: append(baseImagesCommonFlags(),
			&cli.StringFlag{Name: flagLedger, Usage: "publication base-image ledger to admit and project; exclusive with --all-images-json"},
			&cli.StringFlag{Name: "all-images-json", Sources: cli.EnvVars("ALL_IMAGES_JSON"), Usage: "JSON array of base image metadata to verify/promote"},
			&cli.StringFlag{Name: flagBaseInputID, Sources: cli.EnvVars("BASE_INPUT_ID"), Usage: "optional single sha256 base input ID expected for every image"},
			regflags.Username(),
			regflags.PasswordFile(),
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			common, err := baseImagesCommonFromCmd(cmd, true, true)
			if err != nil {
				return err
			}

			if keyErr := validateBaseImagesPublicKey(common.PublicKeyPath, common.PublicKeySHA256); keyErr != nil {
				return keyErr
			}

			images, err := basePromotionImages(cmd, common.ExpectedRepository)
			if err != nil {
				return err
			}

			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				return withBaseImagesDockerConfig(func(authFile string) error {
					if err := common.login(authFile); err != nil {
						return err
					}

					result, err := appbaseimages.PromoteBaseImages(ctx,
						ociregistry.WithAuthFile(authFile),
						cosign.NewIsolated("DOCKER_CONFIG"),
						os.Stderr,
						appbaseimages.BaseImagePromoteInput{
							Images:             images,
							BaseInputID:        cmd.String(flagBaseInputID),
							ExpectedRepository: common.ExpectedRepository,
							ExpectedSource:     common.ExpectedSource,
							ExpectedWorkflow:   common.ExpectedWorkflow,
							CosignPublicKey:    common.PublicKeyPath,
						})
					if err != nil {
						return err
					}

					return writeBaseImagesPromoteOutputs(ctx, dep, result)
				})
			})
		},
	}
}

func basePromotionImages(cmd *cli.Command, repository string) ([]appbaseimages.BaseImageMetadata, error) {
	if cmd.String(flagLedger) == "" {
		return parseBaseImages(cmd.String("all-images-json"))
	}

	if cmd.String("all-images-json") != "" {
		return nil, fmt.Errorf("use --ledger or --all-images-json, not both: %w", errs.ErrUsage)
	}

	body, err := cliio.ReadRegularFile(cmd.String(flagLedger))
	if err != nil {
		return nil, err
	}

	return appbaseimages.PromotionImagesFromLedger(body, repository)
}

func writeBaseImagesPromoteOutputs(ctx context.Context, dep *deps.Deps, result appbaseimages.BaseImagePromoteResult) error {
	if err := dep.OutputSink.Set(ctx, "base_input_id", result.BaseInputID); err != nil {
		return err
	}

	if err := dep.OutputSink.Set(ctx, "base_images_json", result.BaseImagesJSON); err != nil {
		return err
	}

	return dep.OutputSink.Set(ctx, "base_input_ids_json", result.BaseInputIDsJSON)
}
