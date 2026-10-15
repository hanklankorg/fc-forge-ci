// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
)

const (
	flagReleaseImages = "release-images"
	flagMaxDelete     = "max-delete"
	flagDryRun        = "dry-run"
)

func baseImagesPruneCmd() *cli.Command {
	return &cli.Command{
		Name:  "prune",
		Usage: "delete promoted base images no supported release is built on",
		Description: `Retention for the base images a consumer's release images are built FROM.
Supply the complete set of recorded, digest-pinned release image refs whose
bases must remain available. Only supplied refs are verified; an omitted release
cannot protect its base. The keep-set comes from signed SLSA base lineage, not
the internal release-image ledger. Missing or unverifiable lineage aborts the run.

The key file is the trust root. An optional --cosign-public-key-sha256 pin is
checked before external calls. Optional --expected-source and --expected-workflow
constrain every release attestation, in either supported provenance profile;
they are not inferred from the base-builder repository. Omit them when retaining
releases from several consumers signed by the selected trusted key.

The consumer defines base-input IDs and handles rebuilding missing bases.
Pruning does not guarantee that dependencies or tooling remain available for a
rebuild. --max-delete counts base-input IDs; each may have several flavor tags.

Deletion uses the forge package API, or tag-addressed OCI requests with
--local-registry. The latter requires registry support, refuses known shared
manifests, and never falls back to deletion by digest.

--dry-run is the default: run it, read the list, then pass --dry-run=false.

EXAMPLE:
   forge-ci container base-images prune \
     --repository-suffix -base \
     --release-images "$(cat supported-release-images.txt)" \
     --cosign-public-key-path keys/cosign.pub \
     --cosign-public-key-sha256 "$PINNED_KEY_SHA256"`,
		Flags: append(append(baseImagesLocationFlags(), baseImagesPublicKeyFlags()...),
			&cli.StringFlag{Name: flagExpectedSource, Sources: cli.EnvVars("EXPECTED_SOURCE", "BASE_IMAGES_EXPECTED_SOURCE"), Usage: "optional source repository URL required in every release attestation; not derived from the base-image repository"},
			&cli.StringFlag{Name: flagExpectedWorkflow, Sources: cli.EnvVars("EXPECTED_WORKFLOW"), Usage: "optional relative workflow path required in every release attestation"},
			baseImagesRegistryFlag(),
			&cli.StringFlag{
				Name:    flagReleaseImages,
				Sources: cli.EnvVars("RELEASE_IMAGES"),
				Usage:   "newline- or comma-separated digest-pinned release image refs whose bases must be kept",
			},
			&cli.IntFlag{
				Name:    flagMaxDelete,
				Value:   20,
				Sources: cli.EnvVars("BASE_IMAGES_MAX_DELETE"),
				Usage:   "maximum unreferenced base-input IDs per pass; all flavor tags of each ID are removed (0 disables the bound)",
			},
			&cli.BoolFlag{
				Name:    flagDryRun,
				Value:   true,
				Sources: cli.EnvVars("BASE_IMAGES_PRUNE_DRY_RUN"),
				Usage:   "report what would be deleted without deleting it",
			},
			localRegistryFlag(),
			regflags.AuthFile(regflags.AuthFileOpts{Usage: "registry auth file for attestation verification and registry access"}),
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			common, err := baseImagesLocationFromCmd(cmd, false)
			if err != nil {
				return err
			}

			common.PublicKeyPath, common.PublicKeySHA256, err = baseImagesPublicKeyFromCmd(cmd, true)
			if err != nil {
				return err
			}

			if cmd.IsSet("cosign-public-key-sha256") {
				if keyErr := validateBaseImagesPublicKey(common.PublicKeyPath, common.PublicKeySHA256); keyErr != nil {
					return keyErr
				}
			}

			pruner, err := baseImagePackageRegistry(cmd, common, "base-image retention")
			if err != nil {
				return err
			}

			verifier := cosign.NewIsolated("DOCKER_CONFIG")
			verifier.AuthFile = cmd.String(flagAuthFile)

			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				result, err := appbaseimages.PruneBaseImages(ctx,
					pruner,
					verifier,
					os.Stderr,
					appbaseimages.BaseImagePruneInput{
						ExpectedRepository: common.ExpectedRepository,
						ExpectedSource:     strings.TrimSpace(cmd.String(flagExpectedSource)),
						ExpectedWorkflow:   strings.TrimSpace(cmd.String(flagExpectedWorkflow)),
						ReleaseImages:      splitReleaseImages(cmd.String(flagReleaseImages)),
						Attestation: domaincontainer.AttestationVerifyRequest{
							PredicateType: domaincontainer.PredicateTypeSLSAProvenance1,
							KeyRef:        common.PublicKeyPath,
						},
						MaxDelete: cmd.Int(flagMaxDelete),
						DryRun:    cmd.Bool(flagDryRun),
					})
				if err != nil {
					return err
				}

				return writeBaseImagesPruneOutputs(ctx, dep, result)
			})
		},
	}
}

// splitReleaseImages accepts refs one per line or comma-separated, so a caller
// can pass a here-doc, a file, or a workflow output without reshaping it.
func splitReleaseImages(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ' ' || r == '\t'
	})

	refs := make([]string, 0, len(fields))

	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			refs = append(refs, trimmed)
		}
	}

	return refs
}

// writeBaseImagesPruneOutputs publishes the counts a scheduled job wants to
// alert on, and the pruned IDs so a run is auditable from its logs alone.
func writeBaseImagesPruneOutputs(ctx context.Context, dep *deps.Deps, result appbaseimages.BaseImagePruneResult) error {
	if err := dep.OutputSink.Set(ctx, "inventory_count", strconv.Itoa(len(result.Inventory))); err != nil {
		return err
	}

	if err := dep.OutputSink.Set(ctx, "referenced_count", strconv.Itoa(len(result.Referenced))); err != nil {
		return err
	}

	if err := dep.OutputSink.Set(ctx, "prunable_count", strconv.Itoa(len(result.Prunable))); err != nil {
		return err
	}

	if err := dep.OutputSink.Set(ctx, "deleted_count", strconv.Itoa(len(result.Deleted))); err != nil {
		return err
	}

	return dep.OutputSink.Set(ctx, "pruned_base_input_ids", strings.Join(result.Deleted, ","))
}
