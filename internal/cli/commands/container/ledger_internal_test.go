// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

func TestLedgerCleanup_ReportsOnlyDeletedCandidates(t *testing.T) {
	for _, promoted := range []bool{false, true} {
		t.Run(fmt.Sprint("promoted=", promoted), func(t *testing.T) {
			const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

			entry := imageledger.Entry{
				Ref: "127.0.0.1:1/app@" + digest, Digest: digest,
				FinalTag: "127.0.0.1:1/app:v1.2.3", CandidateTag: "127.0.0.1:1/app:staging-v1.2.3",
			}
			reg := &tagStore{tags: map[string]string{entry.CandidateTag: digest}}

			want := 0

			if promoted {
				reg.tags[entry.FinalTag] = digest
				want = 1
			}

			capture, err := os.CreateTemp(t.TempDir(), "stderr")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = capture.Close() }()

			previous := os.Stderr
			os.Stderr = capture //nolint:reassign // serial test captures the command's public diagnostic.

			defer func() { os.Stderr = previous }() //nolint:reassign // restore even after a failed assertion.

			if cleanupErr := runLedgerCleanup(t.Context(), reg, []imageledger.Entry{entry}, "v1.2.3", "ledger", false); cleanupErr != nil {
				t.Fatal(cleanupErr)
			}

			os.Stderr = previous //nolint:reassign // restore before reading the result.

			body, err := os.ReadFile(capture.Name())
			if err != nil || !strings.Contains(string(body), fmt.Sprintf("cleaned up %d candidate tag(s)", want)) || len(reg.operations) != want {
				t.Fatalf("output=%q operations=%v err=%v", body, reg.operations, err)
			}
		})
	}
}

// stubResolver maps refs → digests for the dry-run decorator test.
type stubResolver map[string]string

func (s stubResolver) ResolveDigest(_ context.Context, ref string) (string, error) {
	d, ok := s[ref]
	if !ok {
		return "", errs.ErrMissingInput
	}

	return d, nil
}

func TestDryRunRegistry_SimulatesCopyAndLogsWithoutMutating(t *testing.T) {
	t.Parallel()

	const dig = "sha256:abc"

	var buf bytes.Buffer

	d := newDryRunRegistry(stubResolver{"reg/repo:staging": dig}, &buf)
	ctx := context.Background()

	// A copy is simulated, not performed: it logs and makes the dest
	// resolve to the source digest so the domain's post-copy verify passes.
	if err := d.CopyTag(ctx, "reg/repo:staging", "reg/repo:v1"); err != nil {
		t.Fatal(err)
	}

	if got, _ := d.ResolveDigest(ctx, "reg/repo:v1"); got != dig {
		t.Errorf("simulated dest digest = %q, want %q", got, dig)
	}

	if err := d.DeleteTag(ctx, "reg/repo:staging"); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, "would promote reg/repo:staging -> reg/repo:v1") {
		t.Errorf("missing promote preview: %q", out)
	}

	if !strings.Contains(out, "would delete reg/repo:staging") {
		t.Errorf("missing delete preview: %q", out)
	}
}

// recordingRegistry is a CleanupRegistry+Registry that records the
// mutations it was asked to perform, so the audit decorators can be
// tested for both delegation and narration.
type recordingRegistry struct {
	stubResolver

	copied  []string
	deleted []string
}

func (r *recordingRegistry) CopyTag(_ context.Context, source, dest string) error {
	r.copied = append(r.copied, source+"->"+dest)

	return nil
}

func (r *recordingRegistry) DeleteTag(_ context.Context, ref string) error {
	r.deleted = append(r.deleted, ref)

	return nil
}

// TestAuditDecorators_NarrateAndDelegate is the defense-in-depth audit
// check: a real (non-dry-run) copy/delete must both reach the underlying
// registry AND leave a per-tag line in the log, so destructive mutations
// are as observable as their --dry-run preview.
func TestAuditDecorators_NarrateAndDelegate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rec := &recordingRegistry{stubResolver: stubResolver{}}

	var buf bytes.Buffer

	if err := (auditCopier{Registry: rec, out: &buf}).CopyTag(ctx, "reg/repo:staging", "reg/repo:v1"); err != nil {
		t.Fatal(err)
	}

	if err := (auditDeleter{CleanupRegistry: rec, out: &buf}).DeleteTag(ctx, "reg/repo:staging"); err != nil {
		t.Fatal(err)
	}

	// Delegated to the real registry.
	if len(rec.copied) != 1 || rec.copied[0] != "reg/repo:staging->reg/repo:v1" {
		t.Errorf("copy not delegated: %v", rec.copied)
	}

	if len(rec.deleted) != 1 || rec.deleted[0] != "reg/repo:staging" {
		t.Errorf("delete not delegated: %v", rec.deleted)
	}

	// And narrated to the audit log.
	out := buf.String()
	if !strings.Contains(out, "promoting reg/repo:staging -> reg/repo:v1") {
		t.Errorf("missing promote audit line: %q", out)
	}

	if !strings.Contains(out, "deleting reg/repo:staging") {
		t.Errorf("missing delete audit line: %q", out)
	}
}

// TestLedgerMutatingVerbsHaveDryRun is the guardrail: every destructive
// registry verb must support --dry-run so operators can preview before
// mutating. Grow `required` as --dry-run coverage expands to other verbs.
func TestLedgerMutatingVerbsHaveDryRun(t *testing.T) {
	t.Parallel()

	required := map[string]bool{"promote": true, "cleanup": true, "rollback": true}

	for _, sub := range ledgerGroup().Commands {
		if !required[sub.Name] {
			continue
		}

		if !hasFlag(sub, "dry-run") {
			t.Errorf("ledger %q is a mutating verb and must expose --dry-run", sub.Name)
		}

		delete(required, sub.Name)
	}

	for name := range required {
		t.Errorf("expected ledger subcommand %q not found", name)
	}
}

func TestLedgerPromoteRollbackExposePromotionJournal(t *testing.T) {
	t.Parallel()

	// Tracked rather than merely scanned: a loop that finds neither verb
	// passes silently, so renaming or dropping one would switch the rule off
	// instead of failing it.
	found := map[string]bool{}

	for _, sub := range ledgerGroup().Commands {
		switch sub.Name {
		case "promote", "rollback":
			found[sub.Name] = true

			if !hasFlag(sub, "journal") {
				t.Errorf("ledger %q must expose --journal", sub.Name)
			}
		}
	}

	for _, name := range []string{"promote", "rollback"} {
		if !found[name] {
			t.Errorf("ledger %s command not found", name)
		}
	}
}

func TestLedgerPromote_ExposesDigestRefFallback(t *testing.T) {
	t.Parallel()

	for _, sub := range ledgerGroup().Commands {
		if sub.Name != "promote" {
			continue
		}

		for _, flag := range []string{"allow-digest-ref-fallback", "expected-image-repository"} {
			if !hasFlag(sub, flag) {
				t.Errorf("ledger promote must expose --%s", flag)
			}
		}

		return
	}

	t.Fatal("ledger promote command not found")
}

func TestLedgerRollbackAndCleanupExposeExpectedRepository(t *testing.T) {
	t.Parallel()

	found := map[string]bool{}

	for _, sub := range ledgerGroup().Commands {
		switch sub.Name {
		case "rollback", "cleanup":
			found[sub.Name] = true
			if !hasFlag(sub, "expected-image-repository") {
				t.Errorf("ledger %s must expose --expected-image-repository", sub.Name)
			}
		}
	}

	for _, name := range []string{"rollback", "cleanup"} {
		if !found[name] {
			t.Fatalf("ledger %s command not found", name)
		}
	}
}

func TestLedgerSign_ExposesSignerFlags(t *testing.T) {
	t.Parallel()

	for _, sub := range ledgerGroup().Commands {
		if sub.Name != "sign" {
			continue
		}

		for _, flag := range []string{"ledger", "tag", "provenance-predicate", "provenance-envelope", "method", "key", "recursive", "expected-image-repository", "expected-base-repository", "sbom-path-pattern"} {
			if !hasFlag(sub, flag) {
				t.Errorf("ledger sign missing --%s", flag)
			}
		}

		return
	}

	t.Fatal("ledger sign command not found")
}

// TestLedgerValidate_ExposesAllowEmpty pins the empty-ledger contract on the
// command surface: --allow-empty is the opt-out from the default refusal.
func TestLedgerValidate_ExposesAllowEmpty(t *testing.T) {
	t.Parallel()

	for _, sub := range ledgerGroup().Commands {
		if sub.Name != "validate" {
			continue
		}

		if !hasFlag(sub, "allow-empty") {
			t.Error("ledger validate missing --allow-empty")
		}

		return
	}

	t.Fatal("ledger validate command not found")
}

func TestLedgerAdd_ExposesSBOMPinAndProvenanceFlags(t *testing.T) {
	t.Parallel()

	for _, sub := range ledgerGroup().Commands {
		if sub.Name != "add" {
			continue
		}

		for _, flag := range []string{"sbom-sha256", "provenance-json"} {
			if !hasFlag(sub, flag) {
				t.Errorf("ledger add missing --%s", flag)
			}
		}

		return
	}

	t.Fatal("ledger add command not found")
}

func hasFlag(cmd *cli.Command, name string) bool {
	for _, f := range cmd.Flags {
		for _, n := range f.Names() {
			if n == name {
				return true
			}
		}
	}

	return false
}
