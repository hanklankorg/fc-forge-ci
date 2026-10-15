// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/dryrun"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

func digestRefFallbackFlag() cli.Flag {
	return &cli.BoolFlag{
		Name:    "allow-digest-ref-fallback",
		Sources: cli.EnvVars("PROMOTE_ALLOW_DIGEST_REF_FALLBACK"),
		Usage:   "when candidate_tag is absent or no longer serves the recorded digest, copy from the ledger ref digest instead; recovers a re-run release whose candidate tag has since been cleaned up",
	}
}

func ledgerPromoteCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdPromote,
		Usage: "promote each entry's candidate image to the stage's moving pointer (<base>:<stage>) on the same digest, verifying after each copy; the immutable :<version> tag is build-only",
		Description: `EXAMPLES:
   # Promote to the :release pointer (release scope requires --tag)
   forge-ci container ledger promote --ledger release-images.json --tag v1.2.3 --stage release

   # Preview a staging promotion without touching the registry
   forge-ci container ledger promote --ledger release-images.json --stage staging --dry-run`,
		Flags: []cli.Flag{
			ledgerPathFlag(),
			releaseTagFlag(),
			ledgerAuthFileFlag("registry auth file for the promotion copies and digest verification"),
			stageFlag(),
			stageRepoFlag(),
			releaseTagsFromLedgerFlag(),
			digestRefFallbackFlag(),
			expectedImageRepositoryFlag(),
			promotionJournalFlag(),
			dryRunFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			entries, err := ledgerEntriesFromCmd(cmd)
			if err != nil {
				return err
			}

			stage := imageledger.Stage{
				Name:                   cmd.String("stage"),
				TargetRepo:             cmd.String("stage-repo"),
				UseEntryReleaseTags:    cmd.Bool("release-tags-from-ledger"),
				AllowDigestRefFallback: cmd.Bool("allow-digest-ref-fallback"),
			}

			// A cross-registry destination (--stage-repo) copies the
			// signature via cosign; same-repo promotions never consult it.
			var realSigCopier imageledger.SignatureCopier

			if stage.TargetRepo != "" {
				copier := cosign.New()
				copier.AuthFile = cmd.String(flagAuthFile)
				realSigCopier = cosignSigCopier{cosign: copier, out: os.Stderr}
			}

			reg, sigCopier := promotionRegistries(ledgerRegistry(cmd), dryrun.Enabled(cmd), realSigCopier)

			return appcontainer.PromoteLedger(ctx, appcontainer.LedgerPromotion{
				Registry:        reg,
				SignatureCopier: sigCopier,
				Entries:         entries,
				ReleaseTag:      cmd.String(flagTag),
				Stage:           stage,
				Journal:         cmd.String("journal"),
				JournalDirPerm:  0o755, //nolint:mnd // dist-style state dir; 0755 is conventional.
				DryRun:          dryrun.Enabled(cmd),
				ErrPrefix:       "ledger",
				Out:             os.Stderr,
			})
		},
	}
}

// promotionRegistries builds the promote registry pair: dry-run
// simulates copies in memory and previews them (the simulated registry
// doubles as the signature copier); a real run narrates each copy via
// auditCopier (audit trail) and uses the caller's signature copier
// (cosign for cross-registry promotion, nil when unused).
func promotionRegistries(resolver imageledger.Registry, dryRun bool, realSigCopier imageledger.SignatureCopier) (imageledger.Registry, imageledger.SignatureCopier) {
	if dryRun {
		sim := newDryRunRegistry(resolver, os.Stderr)

		return sim, sim
	}

	return auditCopier{Registry: resolver, out: os.Stderr}, realSigCopier
}
