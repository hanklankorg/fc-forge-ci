// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

// ledgerAddCmd wires `container ledger add`. The use case itself -- entry
// shaping, validation, optional digest capture and the locked append -- is
// appcontainer.AddLedgerEntry; this file reads the flags and builds the
// registry adapter.
func ledgerAddCmd() *cli.Command {
	return &cli.Command{
		Name:  "add",
		Usage: "validate one image entry and append it to the ledger",
		Description: `EXAMPLES:
   # Record a pushed image, capturing its digest from the registry
   forge-ci container ledger add --role distroless \
     --candidate-tag forgejo.example.com/owner/repo:staging-v1.2.3 \
     --final-tag forgejo.example.com/owner/repo:v1.2.3 \
     --sbom dist/image-sbom.cyclonedx.json --tag v1.2.3 --capture-digest

   # Direct-push consumer with an explicit digest
   forge-ci container ledger add --role alpine \
     --ref forgejo.example.com/owner/repo@sha256:… --digest sha256:… \
     --final-tag forgejo.example.com/owner/repo:v1.2.3-alpine \
     --sbom dist/image-sbom-alpine.cyclonedx.json --tag v1.2.3`,
		Flags: []cli.Flag{
			ledgerPathFlag(),
			releaseTagFlag(),
			&cli.StringFlag{Name: "role", Usage: "image role, e.g. distroless, alpine, base"},
			&cli.StringFlag{Name: "image-kind", Value: string(imageledger.ImageKindRelease), Usage: "entry validation scope recorded as image_kind: release (default) or base"},
			&cli.StringFlag{Name: flagRef, Usage: "digest-pinned image ref (registry/path@sha256:<64 hex>)"},
			&cli.StringFlag{Name: flagDigest, Usage: "image digest (sha256:<64 hex>)"},
			&cli.StringFlag{Name: "sbom", Usage: "CycloneDX SBOM path (dist/image-sbom*.cyclonedx.json)"},
			&cli.StringFlag{Name: flagSBOMSHA256, Sources: cli.EnvVars("LEDGER_SBOM_SHA256"), Usage: "sha256 hex pin of the premade --sbom file; ledger sign verifies the pin and attests that exact document instead of generating a fresh one"},
			&cli.StringFlag{Name: "provenance-json", Sources: cli.EnvVars("LEDGER_PROVENANCE_JSON"), Usage: "JSON object of extra externalParameters recorded on the entry and merged into its enriched SLSA predicate at signing; engine-computed keys are reserved (a collision fails the signing run)"},
			&cli.StringFlag{Name: flagImageName, Usage: "image registry/path (no tag); with --tag, derives --final-tag=<image>:<tag> and --candidate-tag=<image>:staging-<tag> so callers don't hand-assemble both (explicit flags still win)"},
			&cli.StringFlag{Name: "final-tag-name", Usage: "immutable release tag name/portion combined with --image-name; when --tag is empty, derives the release tag from vMAJOR.MINOR.PATCH[-suffix]"},
			&cli.StringFlag{Name: "final-tag", Usage: "immutable release tag ref (scoped to --tag); derived from --image-name when omitted"},
			&cli.StringFlag{Name: "moving-tag-name", Usage: "optional moving tag name/portion combined with --image-name"},
			&cli.StringFlag{Name: "moving-tag", Usage: "optional moving tag ref, e.g. forgejo.example.com/owner/repo:rust"},
			&cli.StringFlag{Name: flagFlavor, Usage: "optional base-image flavour"},
			&cli.BoolFlag{Name: "derive-candidate-tag", Sources: cli.EnvVars("LEDGER_DERIVE_CANDIDATE_TAG"), Usage: "derive --candidate-tag as <image>:staging-<final-tag-name>; mutually exclusive with explicit candidate tag flags"},
			&cli.BoolFlag{Name: "default-sbom", Usage: "when --sbom is empty, use dist/image-sbom-<flavor|role>.cyclonedx.json"},
			&cli.StringFlag{Name: "candidate-tag-name", Usage: "optional staging tag name/portion combined with --image-name"},
			&cli.StringFlag{Name: "candidate-tag", Usage: "optional staging tag ref (scoped to staging-<tag>); derived from --image-name when omitted"},
			&cli.StringFlag{Name: "base-ref", Usage: "optional digest-pinned base image"},
			&cli.StringFlag{Name: flagBaseInputID, Usage: "optional base-image input identifier (SLSA lineage)"},
			ledgerAuthFileFlag("registry auth file for --capture-digest"),
			&cli.BoolFlag{Name: "capture-digest", Usage: "resolve the digest from the registry (single source of truth) instead of --ref/--digest; reads the candidate or final tag"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return appcontainer.AddLedgerEntry(ctx, ledgerRegistry(cmd), os.Stderr, appcontainer.AddLedgerEntryInput{
				LedgerPath:          cmd.String(flagLedger),
				CaptureDigest:       cmd.Bool("capture-digest"),
				ReleaseTag:          cmd.String(flagTag),
				Role:                cmd.String("role"),
				ImageKind:           cmd.String("image-kind"),
				ImageName:           cmd.String(flagImageName),
				Ref:                 cmd.String(flagRef),
				Digest:              cmd.String(flagDigest),
				SBOM:                cmd.String("sbom"),
				SBOMSHA256:          cmd.String(flagSBOMSHA256),
				ProvenanceJSON:      cmd.String("provenance-json"),
				DefaultSBOM:         cmd.Bool("default-sbom"),
				FinalTagName:        cmd.String("final-tag-name"),
				FinalTag:            cmd.String("final-tag"),
				MovingTagName:       cmd.String("moving-tag-name"),
				MovingTag:           cmd.String("moving-tag"),
				Flavor:              cmd.String(flagFlavor),
				DeriveCandidateTag:  cmd.Bool("derive-candidate-tag"),
				CandidateTagName:    cmd.String("candidate-tag-name"),
				CandidateTag:        cmd.String("candidate-tag"),
				BaseRef:             cmd.String("base-ref"),
				BaseInputID:         cmd.String(flagBaseInputID),
				LegacyImageNameMode: cmd.String("final-tag-name") == "" && cmd.String("final-tag") == "",
			})
		},
	}
}
