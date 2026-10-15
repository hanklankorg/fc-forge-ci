// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

// LedgerPromotion carries the promote workflow's inputs; `ledger promote` and
// `release-images promote` both build one and call PromoteLedger.
type LedgerPromotion struct {
	Registry        imageledger.Registry
	SignatureCopier imageledger.SignatureCopier
	Entries         []imageledger.Entry
	ReleaseTag      string
	Stage           imageledger.Stage
	Journal         string      // empty: skip the journal write
	JournalDirPerm  os.FileMode // mode for a created journal parent dir
	DryRun          bool        // preview only: simulate copies and write no journal
	ErrPrefix       string      // error/message prefix: "ledger" or "release images"
	Out             io.Writer   // success-line sink (stderr)
}

// PromoteLedger is the shared promote workflow: journal the promotion
// plan first (the rollback source of truth), promote every entry to the
// stage, then report how many destination tags were copied.
func PromoteLedger(ctx context.Context, run LedgerPromotion) error {
	if err := writePromotionJournal(ctx, run); err != nil {
		return err
	}

	copied, err := imageledger.PromoteToStage(ctx, run.Registry, run.SignatureCopier, run.Entries, run.ReleaseTag, run.Stage)
	if err != nil {
		return err
	}

	stage := run.Stage.Name
	if stage == "" {
		stage = imageledger.ReleaseStageName
	}

	verb := "promoted"
	if run.DryRun {
		verb = "would promote"
	}

	_, _ = fmt.Fprintf(run.Out, "%s: %s %d tag(s) to stage %q\n", run.ErrPrefix, verb, copied, stage)

	return nil
}

// writePromotionJournal plans the promotion and writes the journal file
// when a journal path is set; with no journal path it is a no-op.
//
// A dry run writes nothing. The journal is the record of what was actually
// published, and rollback deletes the tags it names: a preview that left one
// behind was indistinguishable from a real promotion, so a later `rollback`
// would act on a promotion that never happened and target the final and moving
// tags — the moving tag being the one that still points at the previous, real
// release.
func writePromotionJournal(ctx context.Context, run LedgerPromotion) error {
	if run.Journal == "" || run.DryRun {
		return nil
	}

	if run.Journal == cliio.StdSentinel {
		return writeNewPromotionJournal(ctx, run)
	}

	if err := os.MkdirAll(filepath.Dir(run.Journal), run.JournalDirPerm); err != nil {
		return fmt.Errorf("%s: create dir for promotion journal %s: %w", run.ErrPrefix, run.Journal, err)
	}

	return cliio.WithLock(run.Journal, func() error { return reuseOrWritePromotionJournal(ctx, run) })
}

func reuseOrWritePromotionJournal(ctx context.Context, run LedgerPromotion) error {
	existing, err := os.ReadFile(run.Journal) //nolint:gosec // operator-supplied journal path is the command's intended input.
	if os.IsNotExist(err) || (err == nil && len(bytes.TrimSpace(existing)) == 0) {
		return writeNewPromotionJournal(ctx, run)
	}

	if err != nil {
		return fmt.Errorf("%s: read promotion journal %s: %w", run.ErrPrefix, run.Journal, err)
	}

	records, err := imageledger.ParsePromotionJournal(existing)
	if err != nil {
		return fmt.Errorf("%s: existing promotion journal %s: %w", run.ErrPrefix, run.Journal, err)
	}

	if err := imageledger.ValidatePromotionJournalIntent(records, run.Entries, run.ReleaseTag, run.Stage); err != nil {
		return fmt.Errorf("%s: existing promotion journal %s does not match this promotion: %w", run.ErrPrefix, run.Journal, err)
	}

	return nil
}

func writeNewPromotionJournal(ctx context.Context, run LedgerPromotion) error {
	records, err := imageledger.PlanReleasePromotionRollback(ctx, run.Registry, run.Entries, run.ReleaseTag, run.Stage)
	if err != nil {
		return err
	}

	body, err := imageledger.MarshalPromotionJournal(records)
	if err != nil {
		return err
	}

	if err := cliio.WriteFile(run.Journal, body, 0o600); err != nil {
		return fmt.Errorf("%s: write promotion journal %s: %w", run.ErrPrefix, run.Journal, err)
	}

	return nil
}
