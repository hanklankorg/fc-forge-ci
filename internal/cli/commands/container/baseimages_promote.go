// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
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
registry.

The required --base-inputs-json constrains every promoted flavor's own input
ID and parent_base in the same verified SLSA statement. Its flavors must
exactly match the target image flavors: missing and extra entries are rejected
before login. "base-images declared-inputs" projects it from the build's
ledger; a caller that authenticated the parent itself passes that parent.
Independently verifying the parent (including cross-repository parents)
remains the caller's duty.`,
		Flags: append(baseImagesCommonFlags(),
			&cli.StringFlag{Name: flagLedger, Usage: "publication base-image ledger to admit and project; exclusive with --all-images-json"},
			&cli.StringFlag{Name: "all-images-json", Sources: cli.EnvVars("ALL_IMAGES_JSON"), Usage: "JSON array of base image metadata to verify/promote"},
			&cli.StringFlag{Name: flagBaseInputsJSON, Required: true, Sources: cli.EnvVars("BASE_INPUTS_JSON"), Usage: "JSON array mapping every promoted flavor to expected base_input_id and expected parent_base {ref,input_id}"},
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

			baseInputs, err := parseBaseInputs(cmd.String(flagBaseInputsJSON))
			if err != nil {
				return err
			}

			in := appbaseimages.BaseImagePromoteInput{
				Images: images, BaseInputs: baseInputs,
				BaseInputID:        cmd.String(flagBaseInputID),
				ExpectedRepository: common.ExpectedRepository,
				ExpectedSource:     common.ExpectedSource,
				ExpectedWorkflow:   common.ExpectedWorkflow,
				CosignPublicKey:    common.PublicKeyPath,
			}
			if err := appbaseimages.ValidateBaseImagePromotion(in); err != nil {
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
						in)
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

func baseImagesDeclaredInputsCmd() *cli.Command {
	return &cli.Command{
		Name:  "declared-inputs",
		Usage: "print the --base-inputs-json map a base-image ledger declares (read-only)",
		Description: `Admits the ledger and prints, as one JSON array, every flavor's
base_input_id and, for a child, the parent_base its build recorded: the
complete map "base-images promote --base-inputs-json" requires. It is the
build's declared lineage, which promotion then requires in each image's own
verified statement; it does not authenticate a parent.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagLedger, Required: true, Usage: "base-image ledger to project"},
			&cli.StringFlag{Name: "expected-repository", Required: true, Usage: "exact base-image repository of every ledger ref and tag"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			body, err := cliio.ReadRegularFile(cmd.String(flagLedger))
			if err != nil {
				return err
			}

			inputs, err := appbaseimages.BaseInputsFromLedger(body, cmd.String("expected-repository"))
			if err != nil {
				return err
			}

			encoded, err := json.Marshal(inputs)
			if err != nil {
				return err
			}

			_, err = fmt.Fprintf(os.Stdout, "%s\n", encoded)

			return err
		},
	}
}
